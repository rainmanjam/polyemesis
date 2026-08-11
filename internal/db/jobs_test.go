package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/jobs"
)

func mustEnqueue(t *testing.T, d *DB, j jobs.Job) *jobs.Job {
	t.Helper()
	out, err := d.EnqueueJob(j)
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	return out
}

func mustGetJob(t *testing.T, d *DB, id int64) *jobs.Job {
	t.Helper()
	j, err := d.GetJob(id)
	if err != nil {
		t.Fatalf("GetJob(%d): %v", id, err)
	}
	return j
}

func TestJobRoundTrips(t *testing.T) {
	d := testDB(t)
	created := mustEnqueue(t, d, jobs.Job{
		Kind:     "transcribe",
		Target:   jobs.RecordingTarget(7),
		Params:   json.RawMessage(`{"track":2,"model":"base.en"}`),
		Priority: jobs.PriorityUser,
		Unique:   true,
	})
	if created.ID == 0 {
		t.Fatal("created job has no id")
	}

	got := mustGetJob(t, d, created.ID)
	if got.Kind != "transcribe" || got.Target != jobs.RecordingTarget(7) {
		t.Errorf("kind/target = %q/%q", got.Kind, got.Target)
	}
	if string(got.Params) != `{"track":2,"model":"base.en"}` {
		t.Errorf("params = %s, want the processor's JSON back byte for byte", got.Params)
	}
	if got.Priority != jobs.PriorityUser || !got.Unique {
		t.Errorf("priority/unique = %d/%v", got.Priority, got.Unique)
	}
	if got.State != jobs.StateQueued {
		t.Errorf("state = %q, want queued", got.State)
	}
	if got.MaxAttempts != jobs.DefaultMaxAttempts {
		t.Errorf("maxAttempts = %d, want the package default", got.MaxAttempts)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("timestamps were not set")
	}
	if !got.StartedAt.IsZero() || !got.FinishedAt.IsZero() {
		t.Error("a job that has not run must have no start or finish time")
	}
}

func TestJobValidationRunsOnWrite(t *testing.T) {
	d := testDB(t)
	tests := []struct {
		name string
		job  jobs.Job
	}{
		{name: "no kind", job: jobs.Job{Target: "recording:1"}},
		{name: "params are not JSON", job: jobs.Job{Kind: "k", Params: json.RawMessage("nope")}},
		{name: "kind too long", job: jobs.Job{Kind: jobs.Kind(strings.Repeat("x", jobs.MaxKindLen+1))}},
		{name: "target too long", job: jobs.Job{Kind: "k", Target: strings.Repeat("x", jobs.MaxTargetLen+1)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := d.EnqueueJob(tc.job); err == nil {
				t.Fatal("EnqueueJob accepted a job it must reject")
			}
		})
	}
}

func TestClaimJobTakesHighestPriorityThenFIFO(t *testing.T) {
	d := testDB(t)
	// Same kind and the same second, so only priority and insertion order can
	// decide: this is the tie-break, not a clock race.
	for _, tc := range []struct {
		target   string
		priority jobs.Priority
	}{
		{target: "normal-first", priority: jobs.PriorityNormal},
		{target: "user-first", priority: jobs.PriorityUser},
		{target: "bulk", priority: jobs.PriorityBulk},
		{target: "normal-second", priority: jobs.PriorityNormal},
		{target: "user-second", priority: jobs.PriorityUser},
	} {
		mustEnqueue(t, d, jobs.Job{Kind: "work", Target: tc.target, Priority: tc.priority})
	}

	want := []string{"user-first", "user-second", "normal-first", "normal-second", "bulk"}
	now := time.Now()
	for i, target := range want {
		j, err := d.ClaimJob([]jobs.Kind{"work"}, now)
		if err != nil {
			t.Fatalf("ClaimJob: %v", err)
		}
		if j == nil {
			t.Fatalf("claim %d returned nothing, want %q", i, target)
		}
		if j.Target != target {
			t.Fatalf("claim %d = %q, want %q (priority first, then the order asked for)", i, j.Target, target)
		}
		// Freed again so the next claim sees a queue with one fewer, not a
		// queue full of running rows.
		if err := d.FinishJob(j.ID, jobs.StateDone, "", now); err != nil {
			t.Fatalf("FinishJob: %v", err)
		}
	}

	j, err := d.ClaimJob([]jobs.Kind{"work"}, now)
	if err != nil || j != nil {
		t.Fatalf("ClaimJob on an empty queue = %v, %v; want nil, nil — idle is not a fault", j, err)
	}
}

func TestClaimJobSkipsWorkItMustNotTake(t *testing.T) {
	d := testDB(t)
	now := time.Now()

	backoff := mustEnqueue(t, d, jobs.Job{Kind: "work", Target: "later", AvailableAt: now.Add(time.Hour)})
	otherKind := mustEnqueue(t, d, jobs.Job{Kind: "other", Target: "not mine"})
	done := mustEnqueue(t, d, jobs.Job{Kind: "work", Target: "finished"})
	if err := d.FinishJob(done.ID, jobs.StateDone, "", now); err != nil {
		t.Fatalf("FinishJob: %v", err)
	}

	if j, err := d.ClaimJob([]jobs.Kind{"work"}, now); err != nil || j != nil {
		t.Fatalf("claimed %v, want nothing: one job is backed off, one is another kind, one is done", j)
	}
	if j, err := d.ClaimJob(nil, now); err != nil || j != nil {
		t.Fatalf("claimed %v with no eligible kinds, want nothing", j)
	}

	// Past its backoff, the same job is claimable.
	j, err := d.ClaimJob([]jobs.Kind{"work"}, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if j == nil || j.ID != backoff.ID {
		t.Fatalf("claimed %v, want the backed-off job once its time came", j)
	}
	if j, err := d.ClaimJob([]jobs.Kind{"other"}, now); err != nil || j == nil || j.ID != otherKind.ID {
		t.Fatalf("claimed %v, want the other kind when it is asked for", j)
	}
}

func TestClaimJobCountsAnAttemptAndMarksItRunning(t *testing.T) {
	d := testDB(t)
	created := mustEnqueue(t, d, jobs.Job{Kind: "work", MaxAttempts: 3})
	now := time.Now()

	j, err := d.ClaimJob([]jobs.Kind{"work"}, now)
	if err != nil || j == nil {
		t.Fatalf("ClaimJob = %v, %v", j, err)
	}
	if j.State != jobs.StateRunning || j.Attempts != 1 {
		t.Fatalf("returned job state/attempts = %s/%d, want running/1", j.State, j.Attempts)
	}

	stored := mustGetJob(t, d, created.ID)
	if stored.State != jobs.StateRunning || stored.Attempts != 1 {
		t.Fatalf("stored state/attempts = %s/%d, want running/1", stored.State, stored.Attempts)
	}
	if stored.StartedAt.IsZero() {
		t.Error("a claimed job must record when it started")
	}
	// A running job is not claimable a second time.
	if again, err := d.ClaimJob([]jobs.Kind{"work"}, now); err != nil || again != nil {
		t.Fatalf("claimed %v, want nothing: it is already running", again)
	}
}

// The important one: rows a dead process left behind must come back.
func TestRequeueRunningJobsResumesWhatACrashInterrupted(t *testing.T) {
	d := testDB(t)
	now := time.Now()

	// A queue mid-flight when the power went: one job on its first attempt,
	// one that has already burned its budget, one merely queued, one finished.
	resumable := mustEnqueue(t, d, jobs.Job{Kind: "transcribe", Target: jobs.RecordingTarget(1), MaxAttempts: 3})
	poison := mustEnqueue(t, d, jobs.Job{Kind: "transcribe", Target: jobs.RecordingTarget(2), MaxAttempts: 1})
	waiting := mustEnqueue(t, d, jobs.Job{Kind: "transcribe", Target: jobs.RecordingTarget(3)})
	finished := mustEnqueue(t, d, jobs.Job{Kind: "transcribe", Target: jobs.RecordingTarget(4)})

	for _, id := range []int64{resumable.ID, poison.ID} {
		j, err := d.ClaimJob([]jobs.Kind{"transcribe"}, now)
		if err != nil || j == nil {
			t.Fatalf("ClaimJob: %v, %v", j, err)
		}
		if j.ID != id {
			t.Fatalf("claimed %d, want %d", j.ID, id)
		}
	}
	if err := d.FinishJob(finished.ID, jobs.StateDone, "", now); err != nil {
		t.Fatalf("FinishJob: %v", err)
	}
	if err := d.UpdateJobProgress(resumable.ID, 0.4, []string{"halfway through track 2"}, now); err != nil {
		t.Fatalf("UpdateJobProgress: %v", err)
	}

	// The server comes back.
	requeued, failed, err := d.RequeueRunningJobs(now.Add(time.Minute))
	if err != nil {
		t.Fatalf("RequeueRunningJobs: %v", err)
	}
	if requeued != 1 || failed != 1 {
		t.Fatalf("recovered %d requeued and %d failed, want 1 and 1", requeued, failed)
	}

	got := mustGetJob(t, d, resumable.ID)
	if got.State != jobs.StateQueued {
		t.Fatalf("interrupted job = %s, want queued: an orphaned running row is work lost forever", got.State)
	}
	if got.Attempts != 1 {
		t.Errorf("attempts = %d, want the interrupted attempt still counted", got.Attempts)
	}
	if got.Error == "" {
		t.Error("the interruption must be recorded, or the operator sees a job that silently restarted")
	}
	if len(got.Log) != 1 {
		t.Errorf("log = %v, want the worker's line to survive the restart", got.Log)
	}
	if !got.StartedAt.IsZero() {
		t.Error("a requeued job must not still claim to have started")
	}

	if got := mustGetJob(t, d, poison.ID); got.State != jobs.StateFailed {
		t.Errorf("a job with no attempts left came back as %s, want failed — requeuing it "+
			"every boot is how one bad job stops the server starting", got.State)
	}
	if got := mustGetJob(t, d, waiting.ID); got.State != jobs.StateQueued {
		t.Errorf("a queued job was disturbed by recovery: %s", got.State)
	}
	if got := mustGetJob(t, d, finished.ID); got.State != jobs.StateDone {
		t.Errorf("a finished job was disturbed by recovery: %s", got.State)
	}

	// And it really is claimable again.
	j, err := d.ClaimJob([]jobs.Kind{"transcribe"}, now.Add(2*time.Minute))
	if err != nil || j == nil || j.ID != resumable.ID {
		t.Fatalf("claimed %v, want the resumed job", j)
	}
	if j.Attempts != 2 {
		t.Errorf("attempts = %d, want the second attempt counted", j.Attempts)
	}
}

func TestRequeueRefundsTheAttemptButRescheduleDoesNot(t *testing.T) {
	d := testDB(t)
	now := time.Now()
	tests := []struct {
		name         string
		act          func(id int64) error
		wantAttempts int
		wantErrText  bool
	}{
		{
			name: "a retryable failure keeps its attempt counted",
			act: func(id int64) error {
				return d.RescheduleJob(id, now.Add(time.Minute), "ffmpeg exited 1", now)
			},
			wantAttempts: 1,
			wantErrText:  true,
		},
		{
			name: "our own shutdown refunds it",
			act: func(id int64) error {
				return d.RequeueJob(id, now, "interrupted by server shutdown", now)
			},
			wantAttempts: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			created := mustEnqueue(t, d, jobs.Job{Kind: jobs.Kind(fmt.Sprintf("k%s", tc.name))})
			j, err := d.ClaimJob([]jobs.Kind{created.Kind}, now)
			if err != nil || j == nil {
				t.Fatalf("ClaimJob: %v, %v", j, err)
			}
			if err := tc.act(created.ID); err != nil {
				t.Fatalf("act: %v", err)
			}
			got := mustGetJob(t, d, created.ID)
			if got.State != jobs.StateQueued {
				t.Fatalf("state = %s, want queued", got.State)
			}
			if got.Attempts != tc.wantAttempts {
				t.Errorf("attempts = %d, want %d", got.Attempts, tc.wantAttempts)
			}
			if (got.Error != "") != tc.wantErrText {
				t.Errorf("error = %q, want it recorded: %v", got.Error, tc.wantErrText)
			}
		})
	}
}

func TestFinishJobIsTerminalAndCompletesTheProgressBar(t *testing.T) {
	d := testDB(t)
	now := time.Now()

	done := mustEnqueue(t, d, jobs.Job{Kind: "k"})
	if err := d.UpdateJobProgress(done.ID, 0.97, nil, now); err != nil {
		t.Fatalf("UpdateJobProgress: %v", err)
	}
	if err := d.FinishJob(done.ID, jobs.StateDone, "", now); err != nil {
		t.Fatalf("FinishJob: %v", err)
	}
	got := mustGetJob(t, d, done.ID)
	if got.Progress != 1 {
		t.Errorf("progress = %v, want a finished job to read 1 even if its worker forgot", got.Progress)
	}
	if got.FinishedAt.IsZero() {
		t.Error("a finished job must record when")
	}

	failed := mustEnqueue(t, d, jobs.Job{Kind: "k"})
	if err := d.UpdateJobProgress(failed.ID, 0.3, nil, now); err != nil {
		t.Fatalf("UpdateJobProgress: %v", err)
	}
	if err := d.FinishJob(failed.ID, jobs.StateFailed, "input file is gone", now); err != nil {
		t.Fatalf("FinishJob: %v", err)
	}
	if got := mustGetJob(t, d, failed.ID); got.Progress != 0.3 || got.Error != "input file is gone" {
		t.Errorf("failed job progress/error = %v/%q, want how far it got and why it stopped", got.Progress, got.Error)
	}

	if err := d.FinishJob(done.ID, jobs.StateQueued, "", now); err == nil {
		t.Error("FinishJob accepted a state that is not terminal")
	}
	if err := d.FinishJob(9999, jobs.StateDone, "", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("FinishJob on a missing job = %v, want ErrNotFound", err)
	}
}

func TestFindActiveJobIgnoresFinishedOnes(t *testing.T) {
	d := testDB(t)
	now := time.Now()
	target := jobs.RecordingTarget(11)

	old := mustEnqueue(t, d, jobs.Job{Kind: "transcribe", Target: target})
	if err := d.FinishJob(old.ID, jobs.StateDone, "", now); err != nil {
		t.Fatalf("FinishJob: %v", err)
	}
	if got, err := d.FindActiveJob("transcribe", target); err != nil || got != nil {
		t.Fatalf("FindActiveJob = %v, %v; want nothing once the old one finished", got, err)
	}

	fresh := mustEnqueue(t, d, jobs.Job{Kind: "transcribe", Target: target})
	got, err := d.FindActiveJob("transcribe", target)
	if err != nil {
		t.Fatalf("FindActiveJob: %v", err)
	}
	if got == nil || got.ID != fresh.ID {
		t.Fatalf("FindActiveJob = %v, want the active job %d", got, fresh.ID)
	}
	if got, err := d.FindActiveJob("proxy", target); err != nil || got != nil {
		t.Fatalf("FindActiveJob for another kind = %v, want nothing: kinds do not dedupe each other", got)
	}
}

func TestCancelDeferAndRetryGuardTheStatesTheyApplyTo(t *testing.T) {
	d := testDB(t)
	now := time.Now()

	t.Run("a queued job can be cancelled", func(t *testing.T) {
		j := mustEnqueue(t, d, jobs.Job{Kind: "c1"})
		if err := d.CancelJob(j.ID, now); err != nil {
			t.Fatalf("CancelJob: %v", err)
		}
		if got := mustGetJob(t, d, j.ID); got.State != jobs.StateCancelled {
			t.Fatalf("state = %s, want cancelled", got.State)
		}
		if err := d.CancelJob(j.ID, now); err == nil {
			t.Error("cancelling a cancelled job must say why it cannot")
		}
	})

	t.Run("a running job can be cancelled", func(t *testing.T) {
		j := mustEnqueue(t, d, jobs.Job{Kind: "c2"})
		if _, err := d.ClaimJob([]jobs.Kind{"c2"}, now); err != nil {
			t.Fatalf("ClaimJob: %v", err)
		}
		if err := d.CancelJob(j.ID, now); err != nil {
			t.Fatalf("CancelJob: %v", err)
		}
	})

	t.Run("a running job cannot be deferred", func(t *testing.T) {
		j := mustEnqueue(t, d, jobs.Job{Kind: "c3"})
		if _, err := d.ClaimJob([]jobs.Kind{"c3"}, now); err != nil {
			t.Fatalf("ClaimJob: %v", err)
		}
		// Deferring running work would mean killing it, which is a decision
		// for a human through Cancel.
		if err := d.DeferJob(j.ID, now.Add(time.Minute), "load", now); err == nil {
			t.Error("DeferJob threw away a running job")
		}
	})

	t.Run("a queued job defers and comes back on its own", func(t *testing.T) {
		j := mustEnqueue(t, d, jobs.Job{Kind: "c4"})
		if err := d.DeferJob(j.ID, now.Add(time.Minute), "yielding to the live stream", now); err != nil {
			t.Fatalf("DeferJob: %v", err)
		}
		got := mustGetJob(t, d, j.ID)
		if got.State != jobs.StateDeferred {
			t.Fatalf("state = %s, want deferred", got.State)
		}
		if len(got.Log) != 1 {
			t.Errorf("log = %v, want the reason recorded", got.Log)
		}
		if claimed, _ := d.ClaimJob([]jobs.Kind{"c4"}, now); claimed != nil {
			t.Fatal("a deferred job was claimed before its deadline")
		}
		claimed, err := d.ClaimJob([]jobs.Kind{"c4"}, now.Add(2*time.Minute))
		if err != nil || claimed == nil {
			t.Fatalf("claimed %v, want the deferred job once its deadline passed", claimed)
		}
	})

	t.Run("only a terminal job can be retried", func(t *testing.T) {
		j := mustEnqueue(t, d, jobs.Job{Kind: "c5", MaxAttempts: 2})
		if err := d.RetryJob(j.ID, now); err == nil {
			t.Error("RetryJob re-armed a job that had not finished")
		}
		if _, err := d.ClaimJob([]jobs.Kind{"c5"}, now); err != nil {
			t.Fatalf("ClaimJob: %v", err)
		}
		if err := d.FinishJob(j.ID, jobs.StateFailed, "disk full", now); err != nil {
			t.Fatalf("FinishJob: %v", err)
		}
		if err := d.RetryJob(j.ID, now); err != nil {
			t.Fatalf("RetryJob: %v", err)
		}
		got := mustGetJob(t, d, j.ID)
		if got.State != jobs.StateQueued || got.Attempts != 0 || got.Error != "" {
			t.Fatalf("retried job = %s attempts %d error %q, want a fresh budget",
				got.State, got.Attempts, got.Error)
		}
	})

	t.Run("a missing job is not found", func(t *testing.T) {
		if err := d.CancelJob(9999, now); !errors.Is(err, ErrNotFound) {
			t.Errorf("CancelJob = %v, want ErrNotFound", err)
		}
		if err := d.RetryJob(9999, now); !errors.Is(err, ErrNotFound) {
			t.Errorf("RetryJob = %v, want ErrNotFound", err)
		}
		if err := d.DeferJob(9999, now, "", now); !errors.Is(err, ErrNotFound) {
			t.Errorf("DeferJob = %v, want ErrNotFound", err)
		}
	})
}

func TestUpdateJobProgressAppendsAndBoundsTheLog(t *testing.T) {
	d := testDB(t)
	now := time.Now()
	j := mustEnqueue(t, d, jobs.Job{Kind: "k"})

	if err := d.UpdateJobProgress(j.ID, 0.25, []string{"first"}, now); err != nil {
		t.Fatalf("UpdateJobProgress: %v", err)
	}
	if err := d.UpdateJobProgress(j.ID, -1, []string{"second"}, now); err != nil {
		t.Fatalf("UpdateJobProgress: %v", err)
	}
	got := mustGetJob(t, d, j.ID)
	if got.Progress != 0.25 {
		t.Errorf("progress = %v, want a negative report to leave it alone", got.Progress)
	}
	if len(got.Log) != 2 || got.Log[0] != "first" || got.Log[1] != "second" {
		t.Fatalf("log = %v, want both lines in order", got.Log)
	}

	// Progress out of range is clamped, not rejected: a worker's arithmetic
	// must never be able to fail a job that is working.
	if err := d.UpdateJobProgress(j.ID, 42, nil, now); err != nil {
		t.Fatalf("UpdateJobProgress: %v", err)
	}
	if got := mustGetJob(t, d, j.ID); got.Progress != 1 {
		t.Errorf("progress = %v, want it clamped to 1", got.Progress)
	}

	flood := make([]string, jobs.MaxLogLines+25)
	for i := range flood {
		flood[i] = fmt.Sprintf("line %d", i)
	}
	if err := d.UpdateJobProgress(j.ID, -1, flood, now); err != nil {
		t.Fatalf("UpdateJobProgress: %v", err)
	}
	got = mustGetJob(t, d, j.ID)
	if len(got.Log) != jobs.MaxLogLines {
		t.Fatalf("log kept %d lines, want the tail bounded at %d", len(got.Log), jobs.MaxLogLines)
	}
	if got.Log[len(got.Log)-1] != flood[len(flood)-1] {
		t.Errorf("last line = %q, want the newest kept", got.Log[len(got.Log)-1])
	}

	if err := d.UpdateJobProgress(9999, 0.5, nil, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateJobProgress on a missing job = %v, want ErrNotFound", err)
	}
}

func TestSetJobResultRejectsWhatIsNotJSON(t *testing.T) {
	d := testDB(t)
	now := time.Now()
	j := mustEnqueue(t, d, jobs.Job{Kind: "k"})

	if err := d.SetJobResult(j.ID, json.RawMessage(`{"path":"/rec/1.srt"}`), now); err != nil {
		t.Fatalf("SetJobResult: %v", err)
	}
	if got := mustGetJob(t, d, j.ID); string(got.Result) != `{"path":"/rec/1.srt"}` {
		t.Errorf("result = %s", got.Result)
	}
	if err := d.SetJobResult(j.ID, json.RawMessage("{oops"), now); err == nil {
		t.Error("SetJobResult stored something that is not JSON")
	}
	if err := d.SetJobResult(9999, json.RawMessage("{}"), now); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetJobResult on a missing job = %v, want ErrNotFound", err)
	}
}

func TestListJobsFiltersAndCounts(t *testing.T) {
	d := testDB(t)
	now := time.Now()

	a := mustEnqueue(t, d, jobs.Job{Kind: "transcribe", Target: jobs.RecordingTarget(1)})
	mustEnqueue(t, d, jobs.Job{Kind: "proxy", Target: jobs.RecordingTarget(1)})
	mustEnqueue(t, d, jobs.Job{Kind: "transcribe", Target: jobs.RecordingTarget(2)})
	if err := d.FinishJob(a.ID, jobs.StateDone, "", now); err != nil {
		t.Fatalf("FinishJob: %v", err)
	}

	tests := []struct {
		name   string
		filter jobs.Filter
		want   int
	}{
		{name: "everything", want: 3},
		{name: "by kind", filter: jobs.Filter{Kinds: []jobs.Kind{"transcribe"}}, want: 2},
		{name: "by target", filter: jobs.Filter{Target: jobs.RecordingTarget(1)}, want: 2},
		{name: "by state", filter: jobs.Filter{States: []jobs.State{jobs.StateDone}}, want: 1},
		{name: "active only", filter: jobs.Active(), want: 2},
		{name: "limited", filter: jobs.Filter{Limit: 1}, want: 1},
		{
			name:   "kind and target together",
			filter: jobs.Filter{Kinds: []jobs.Kind{"transcribe"}, Target: jobs.RecordingTarget(1)},
			want:   1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := d.ListJobs(tc.filter)
			if err != nil {
				t.Fatalf("ListJobs: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("got %d jobs, want %d", len(got), tc.want)
			}
		})
	}

	counts, err := d.JobCounts()
	if err != nil {
		t.Fatalf("JobCounts: %v", err)
	}
	if counts[jobs.StateQueued] != 2 || counts[jobs.StateDone] != 1 {
		t.Errorf("counts = %v, want 2 queued and 1 done", counts)
	}
}

func TestPurgeJobsKeepsTheNewestAndSparesLiveWork(t *testing.T) {
	d := testDB(t)
	now := time.Now()

	finishAt := func(kind jobs.Kind, ago time.Duration) int64 {
		t.Helper()
		j := mustEnqueue(t, d, jobs.Job{Kind: kind})
		if err := d.FinishJob(j.ID, jobs.StateDone, "", now.Add(-ago)); err != nil {
			t.Fatalf("FinishJob: %v", err)
		}
		return j.ID
	}
	oldest := finishAt("k", 3*time.Hour)
	middle := finishAt("k", 2*time.Hour)
	newest := finishAt("k", time.Hour)
	queued := mustEnqueue(t, d, jobs.Job{Kind: "k"})
	running := mustEnqueue(t, d, jobs.Job{Kind: "later"})
	if _, err := d.ClaimJob([]jobs.Kind{"later"}, now); err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}

	n, err := d.PurgeJobs(now.Add(-30*time.Minute), 1)
	if err != nil {
		t.Fatalf("PurgeJobs: %v", err)
	}
	if n != 2 {
		t.Fatalf("purged %d, want the two oldest finished jobs", n)
	}
	for _, id := range []int64{oldest, middle} {
		if _, err := d.GetJob(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("job %d survived the purge", id)
		}
	}
	for _, id := range []int64{newest, queued.ID, running.ID} {
		if _, err := d.GetJob(id); err != nil {
			t.Errorf("job %d was purged but must not be: %v", id, err)
		}
	}
}

// The jobs tables are new, so a database created before them must gain them on
// the next start, and every start after that must be a no-op.
func TestJobSchemaSurvivesRepeatedOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	for i := 0; i < 3; i++ {
		d, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if _, err := d.EnqueueJob(jobs.Job{Kind: "k", Target: jobs.RecordingTarget(int64(i + 1))}); err != nil {
			t.Fatalf("enqueue on open %d: %v", i, err)
		}
		list, err := d.ListJobs(jobs.Filter{})
		if err != nil {
			t.Fatalf("ListJobs on open %d: %v", i, err)
		}
		if len(list) != i+1 {
			t.Fatalf("open %d sees %d jobs, want %d — the queue must outlive the process", i, len(list), i+1)
		}
		d.Close()
	}
}

func TestDeleteJob(t *testing.T) {
	d := testDB(t)
	j := mustEnqueue(t, d, jobs.Job{Kind: "k"})
	if err := d.DeleteJob(j.ID); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	if _, err := d.GetJob(j.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetJob after delete = %v, want ErrNotFound", err)
	}
	if err := d.DeleteJob(j.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteJob twice = %v, want ErrNotFound", err)
	}
}

// TestDeleteJobRemovesOnlyThatRow is the scoping half TestDeleteJob above
// cannot express.
//
// That test seeds exactly ONE row. With n=1, "delete the row you were asked
// for" and "delete every row in the table" are the same observation: the target
// is gone and a second delete reports ErrNotFound either way. Dropping the WHERE
// clause from DeleteJob's statement therefore survives it, and — measured — the
// whole ./internal/... suite. A witness row is the entire point of this test.
func TestDeleteJobRemovesOnlyThatRow(t *testing.T) {
	d := testDB(t)
	target := mustEnqueue(t, d, jobs.Job{Kind: "target"})
	witness := mustEnqueue(t, d, jobs.Job{Kind: "witness"})

	if err := d.DeleteJob(target.ID); err != nil {
		t.Fatalf("DeleteJob(%d): %v", target.ID, err)
	}
	if _, err := d.GetJob(target.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetJob(target %d) after delete = %v, want ErrNotFound", target.ID, err)
	}
	if _, err := d.GetJob(witness.ID); err != nil {
		t.Fatalf("witness job %d was deleted too: deleting one job took the whole table with it (%v)",
			witness.ID, err)
	}
	// The witness must still be deletable in its own right, which is what says
	// the row above is really there rather than merely unreadable.
	if err := d.DeleteJob(witness.ID); err != nil {
		t.Errorf("DeleteJob(witness %d): %v", witness.ID, err)
	}
	if err := d.DeleteJob(witness.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteJob(witness) twice = %v, want ErrNotFound", err)
	}
}

// TestPurgeSparesJobsFinishedSinceTheCutoff pins the CUTOFF, which
// TestPurgeJobsKeepsTheNewestAndSparesLiveWork above does not.
//
// There, every terminal row is older than the cutoff (3h/2h/1h against 30
// minutes) and keep=1 does all the work, so a purge whose date predicate was
// inert would still report 2 and still spare the newest. Here keep is 0 and the
// only thing that can spare a row is its finished_at, so the date predicate is
// the sole survivor-selecting mechanism under test.
//
// The unset-finished_at row is the second half: a job in a terminal state that
// never recorded when it ended has an epoch timestamp, which is older than every
// cutoff. `finished_at > 0` is what stops it being swept, and nothing else does.
func TestPurgeSparesJobsFinishedSinceTheCutoff(t *testing.T) {
	d := testDB(t)
	now := time.Now()
	cutoff := now.Add(-90 * time.Minute)

	finishAt := func(kind jobs.Kind, ago time.Duration) int64 {
		t.Helper()
		j := mustEnqueue(t, d, jobs.Job{Kind: kind})
		if err := d.FinishJob(j.ID, jobs.StateDone, "", now.Add(-ago)); err != nil {
			t.Fatalf("FinishJob: %v", err)
		}
		return j.ID
	}
	oldA := finishAt("old-a", 3*time.Hour)
	oldB := finishAt("old-b", 2*time.Hour)
	freshA := finishAt("fresh-a", time.Hour)
	freshB := finishAt("fresh-b", 30*time.Minute)

	// Terminal, but with no finishing timestamp: FinishJob is bypassed so the
	// column stays 0. This is the row `finished_at > 0` exists for.
	unfinished := mustEnqueue(t, d, jobs.Job{Kind: "no-timestamp"})
	if _, err := d.SQL().Exec(
		`UPDATE jobs SET state='cancelled', finished_at=0 WHERE id=?`, unfinished.ID); err != nil {
		t.Fatalf("seed terminal row without a finished_at: %v", err)
	}

	n, err := d.PurgeJobs(cutoff, 0)
	if err != nil {
		t.Fatalf("PurgeJobs: %v", err)
	}
	if n != 2 {
		t.Errorf("purged %d rows, want exactly the 2 that finished before the cutoff", n)
	}
	for _, id := range []int64{oldA, oldB} {
		if _, err := d.GetJob(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("job %d finished before the cutoff but survived the purge: %v", id, err)
		}
	}
	for _, id := range []int64{freshA, freshB} {
		if _, err := d.GetJob(id); err != nil {
			t.Errorf("job %d finished AFTER the cutoff but was purged: the purge is not "+
				"reading finished_at at all (%v)", id, err)
		}
	}
	if _, err := d.GetJob(unfinished.ID); err != nil {
		t.Errorf("job %d is terminal with no finished_at and was purged; an unset "+
			"timestamp is not evidence of age (%v)", unfinished.ID, err)
	}
}
