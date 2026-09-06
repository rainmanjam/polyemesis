package db

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// TWO ACTIVE JOBS FOR ONE UNIQUE TARGET MUST BE IMPOSSIBLE, not merely
// unlikely.
//
// The rule used to live entirely in queue.Submit: ask FindActiveJob, and
// insert only if it found nothing. Two statements with nothing holding the gap
// between them. db.go's SetMaxOpenConns(1) makes each statement atomic and
// does nothing at all for the interval, so two people clicking Transcribe on
// one recording at the same moment both searched, both found nothing, and both
// inserted -- two FFmpeg processes doing identical work, writing over each
// other's output, and an operator with two rows in the jobs list where the
// product says there can only be one.
//
// This test goes AROUND Submit and calls the store twice directly, because
// Submit's check is exactly the thing that cannot be trusted to hold under a
// race: a test that went through Submit would be testing the guard the defect
// slips past. Calling EnqueueJob twice with no search in between is the race,
// deterministically.
func TestASecondActiveJobForOneUniqueTargetIsRefusedByTheDatabase(t *testing.T) {
	d := testDB(t)
	target := jobs.RecordingTarget(42)

	first := mustEnqueue(t, d, jobs.Job{Kind: "transcribe", Target: target, Unique: true})

	second, err := d.EnqueueJob(jobs.Job{Kind: "transcribe", Target: target, Unique: true})
	if err != nil {
		t.Fatalf("EnqueueJob on a duplicate unique target = %v; the second "+
			"submission must be folded into the first, not turned into an error "+
			"an operator sees on a double click", err)
	}
	if second == nil || second.ID != first.ID {
		t.Fatalf("EnqueueJob returned %v, want the job that was already active (%d)",
			second, first.ID)
	}

	list, err := d.ListJobs(jobs.Filter{Target: target})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("%d rows for target %s, want exactly 1: the database is what has "+
			"to refuse the second, because the check in queue.Submit happens one "+
			"statement earlier than the insert", len(list), target)
	}

	// POSITIVE CONTROL. Everything above would also pass against a store that
	// refused every second job of any shape, which would be a far worse bug
	// than the one being fixed -- a queue that can hold one job. Each of these
	// is a submission the product is required to accept, and each is refused
	// by an index whose predicate is one grain wider than queue.Submit's own
	// rule.
	//
	// A NEW ROW, not merely a nil error, and the difference is the whole
	// control. EnqueueJob answers a refusal by handing back the job that is
	// already active, so an index that is too wide does not produce an error at
	// all: it produces a second click on Clip silently returning somebody
	// else's export. That was measured -- dropping `unique_target = 1` from the
	// predicate left this loop green when it only checked err. So every case
	// here has to come back with an id of its own.
	seen := map[int64]string{first.ID: "the first transcribe job"}
	for _, tc := range []struct {
		name string
		job  jobs.Job
		why  string
	}{
		{
			name: "another kind for the same target",
			job:  jobs.Job{Kind: "proxy", Target: target, Unique: true},
			why: "kinds do not dedupe each other: transcribing a recording and " +
				"making a proxy of it are two jobs, not one",
		},
		{
			name: "the same kind for another target",
			job:  jobs.Job{Kind: "transcribe", Target: jobs.RecordingTarget(43), Unique: true},
			why:  "two recordings are two jobs",
		},
		{
			name: "a job that did not ask to be unique",
			job:  jobs.Job{Kind: "clip", Target: target},
			why: "Unique is opt-in, and clips deliberately do not take it: two " +
				"in-points out of one recording are two different exports",
		},
		{
			name: "a second job that did not ask to be unique",
			job:  jobs.Job{Kind: "clip", Target: target},
			why:  "the same reason, for the second of the pair",
		},
		{
			name: "a unique job with no target",
			job:  jobs.Job{Kind: "sweep", Unique: true},
			why: "an empty target is not something two jobs can share, and " +
				"queue.Submit does not fold on one either",
		},
		{
			name: "a second unique job with no target",
			job:  jobs.Job{Kind: "sweep", Unique: true},
			why:  "the same reason, for the second of the pair",
		},
	} {
		got, err := d.EnqueueJob(tc.job)
		if err != nil {
			t.Errorf("EnqueueJob(%s) = %v, want it accepted: %s", tc.name, err, tc.why)
			continue
		}
		if other, folded := seen[got.ID]; folded {
			t.Errorf("EnqueueJob(%s) handed back %s (job %d) instead of storing a "+
				"new job: %s. The index is refusing rows it must not, and because "+
				"EnqueueJob answers a refusal with the active job the caller sees "+
				"a success rather than an error.", tc.name, other, got.ID, tc.why)
			continue
		}
		seen[got.ID] = tc.name
	}
}

// A FINISHED JOB DOES NOT BLOCK THE NEXT ONE. The index covers the three
// unfinished states and nothing else, so re-transcribing a recording tomorrow
// has to work -- and would not if the predicate had been written over the
// whole table.
//
// Failing to check this would be the expensive half of getting the index
// wrong: it fails not at the moment of the change but weeks later, on the
// first repeat of a job that has already succeeded, as "nothing happens when I
// click it".
func TestAFinishedUniqueJobDoesNotBlockTheNextSubmission(t *testing.T) {
	d := testDB(t)
	now := time.Now()
	target := jobs.RecordingTarget(7)

	for _, terminal := range []jobs.State{jobs.StateDone, jobs.StateFailed, jobs.StateCancelled} {
		j, err := d.EnqueueJob(jobs.Job{Kind: "transcribe", Target: target, Unique: true})
		if err != nil {
			t.Fatalf("EnqueueJob after a %s job: %v", terminal, err)
		}
		if err := d.FinishJob(j.ID, terminal, "", now); err != nil {
			t.Fatalf("FinishJob(%s): %v", terminal, err)
		}
		next, err := d.EnqueueJob(jobs.Job{Kind: "transcribe", Target: target, Unique: true})
		if err != nil {
			t.Fatalf("EnqueueJob once the previous job was %s: %v", terminal, err)
		}
		if next.ID == j.ID {
			t.Fatalf("a %s job was handed back as though it were still active; "+
				"the index must cover queued, running and deferred only", terminal)
		}
		if err := d.FinishJob(next.ID, jobs.StateDone, "", now); err != nil {
			t.Fatalf("FinishJob: %v", err)
		}
	}
}

// THE UPGRADE PATH, which is the one that has real rows in it.
//
// Every install running today was built without this index and may well hold a
// pair the old race produced. Creating the index over such a table fails, and a
// failure inside Open is a server that will not start. So the migration folds
// the duplicates first, and this is that fold happening to a database that was
// built exactly as the current release builds one and then had the race's
// output inserted into it by hand.
//
// The fixture is deliberately built through schemaSQL rather than through
// Open: Open is what installs the index, so a database that has been through
// it cannot hold the duplicates this test is about.
func TestOpeningADatabaseThatAlreadyHoldsDuplicateUniqueJobsFoldsThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := raw.Exec(schemaSQL); err != nil {
		raw.Close()
		t.Fatalf("apply schema: %v", err)
	}
	// Three rows the race could really have produced: two queued and one
	// running, all claiming the same target, plus an untouchable bystander.
	if _, err := raw.Exec(`INSERT INTO jobs
		(id, kind, target, unique_target, state, created_at, updated_at) VALUES
		(1, 'transcribe', 'recording:9', 1, 'queued',  1000, 1000),
		(2, 'transcribe', 'recording:9', 1, 'running', 1001, 1001),
		(3, 'transcribe', 'recording:9', 1, 'queued',  1002, 1002),
		(4, 'transcribe', 'recording:8', 1, 'queued',  1003, 1003),
		(5, 'clip',       'recording:9', 0, 'queued',  1004, 1004),
		(6, 'clip',       'recording:9', 0, 'queued',  1005, 1005)`); err != nil {
		raw.Close()
		t.Fatalf("seed the duplicates the old race produced: %v", err)
	}
	// The index must be impossible to create at this point, or the fixture is
	// not reproducing the state the migration exists for and everything below
	// proves nothing.
	if _, err := raw.Exec(`CREATE UNIQUE INDEX probe ON jobs(kind, target)
		WHERE unique_target = 1 AND target <> ''
		  AND state IN ('queued','running','deferred')`); err == nil {
		raw.Close()
		t.Fatal("the seeded rows do NOT violate the index, so this test is not " +
			"exercising the fold at all")
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a database holding duplicate unique jobs: %v. An "+
			"operator upgrading into this release cannot start the server at all.", err)
	}
	t.Cleanup(func() { d.Close() })

	// The lowest id survives, because that is the job FindActiveJob has always
	// returned and therefore the one anybody watching this target is watching.
	if got := mustGetJob(t, d, 1); got.State != jobs.StateQueued {
		t.Errorf("job 1 = %s, want the lowest id left exactly as it was", got.State)
	}
	for _, id := range []int64{2, 3} {
		got := mustGetJob(t, d, id)
		if got.State != jobs.StateCancelled {
			t.Errorf("job %d = %s, want cancelled: it is a duplicate of job 1", id, got.State)
		}
		if got.Error == "" {
			t.Errorf("job %d was cancelled with no reason recorded; an operator "+
				"looking at the list has to be able to see why it stopped", id)
		}
	}
	// Untouched: a different target, and two jobs that never asked to be unique.
	for _, id := range []int64{4, 5, 6} {
		if got := mustGetJob(t, d, id); got.State != jobs.StateQueued {
			t.Errorf("job %d = %s, want it left alone: the fold must only reach "+
				"rows the index would refuse", id, got.State)
		}
	}

	// And the index is now there, doing the job the fold made room for.
	has, err := indexExists(d.sql, jobUniqueTargetIndex)
	if err != nil {
		t.Fatalf("indexExists: %v", err)
	}
	if !has {
		t.Fatal("the duplicates were folded but the index was not created, so the " +
			"next race produces the same pair again")
	}
	again, err := d.EnqueueJob(jobs.Job{Kind: "transcribe", Target: "recording:9", Unique: true})
	if err != nil {
		t.Fatalf("EnqueueJob after the migration: %v", err)
	}
	if again.ID != 1 {
		t.Errorf("EnqueueJob returned job %d, want the surviving job 1", again.ID)
	}

	// Idempotent, because Open runs it again on every subsequent start.
	if err := d.MigrateJobUniqueTarget(); err != nil {
		t.Fatalf("MigrateJobUniqueTarget (second call): %v", err)
	}
}

// A worker that is still running one of the folded duplicates must be able to
// finish it. The fold moves a row to cancelled; the worker's terminal write
// finds it by id and moves it on to done or failed, and neither state is one
// the index covers, so nothing refuses the write.
//
// This is the half of the fold that is easy to get wrong by being careful: a
// migration that "protected" a running duplicate by leaving it alone could not
// create the index at all, and Open would fail on precisely the installs that
// most need the fix.
func TestAWorkerCanStillFinishADuplicateJobThatWasFoldedUnderIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := raw.Exec(schemaSQL); err != nil {
		raw.Close()
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO jobs
		(id, kind, target, unique_target, state, created_at, updated_at) VALUES
		(1, 'transcribe', 'recording:9', 1, 'running', 1000, 1000),
		(2, 'transcribe', 'recording:9', 1, 'running', 1001, 1001)`); err != nil {
		raw.Close()
		t.Fatalf("seed two running duplicates: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open with two RUNNING duplicates: %v. Two workers already at "+
			"work on one target is the worst case of the old race and the one "+
			"most likely to be in a real database.", err)
	}
	t.Cleanup(func() { d.Close() })

	if err := d.FinishJob(2, jobs.StateDone, "", time.Now()); err != nil {
		t.Fatalf("FinishJob on the folded duplicate: %v. Its worker is still "+
			"running and has to be able to report what happened.", err)
	}
	if got := mustGetJob(t, d, 2); got.State != jobs.StateDone {
		t.Errorf("job 2 = %s, want done", got.State)
	}
	if got := mustGetJob(t, d, 1); got.State != jobs.StateRunning {
		t.Errorf("job 1 = %s, want the survivor untouched", got.State)
	}
}

// RETRY IS THE SECOND WRITER THAT CAN BE REFUSED BY THE INDEX, and the patch
// that installed the index missed it (#746).
//
// RetryJob's WHERE names the three terminal states; its SET writes 'queued'.
// Every one of those terminal states is outside the index's predicate and
// 'queued' is inside it, so a retry is an INSERT as far as the index is
// concerned -- and the whole reason EnqueueJob has a fold is that such an
// insert can be refused.
//
// Unhandled it reached internal/api's writeStoreError as an unrecognised error
// and came back as HTTP 500 with "UNIQUE constraint failed: jobs.kind,
// jobs.target (2067)" as the body. That is, exactly, the outcome EnqueueJob's
// own comment says must not happen: an operator told the server is broken, in
// the vocabulary of an index. The answer has to be ErrStateConflict, which
// writeStoreError maps to 409, naming the job that is in the way.
func TestRetryingATerminalJobWhoseTargetIsNowTakenIsAConflictNotACrash(t *testing.T) {
	d := testDB(t)
	target := jobs.RecordingTarget(9)
	now := time.Now()

	// The shape an operator produces without doing anything unusual: a unique
	// job fails, and they resubmit. The resubmission is ALLOWED -- 'failed' is
	// outside the predicate, so the new job is not a duplicate of anything --
	// and that is what puts a second active job on the target.
	first := mustEnqueue(t, d, jobs.Job{Kind: "transcribe", Target: target, Unique: true})
	if err := d.FinishJob(first.ID, jobs.StateFailed, "the disk was full", now); err != nil {
		t.Fatalf("FinishJob: %v", err)
	}
	second := mustEnqueue(t, d, jobs.Job{Kind: "transcribe", Target: target, Unique: true})
	if second.ID == first.ID {
		t.Fatalf("the resubmission was folded into the failed job (%d); a failed "+
			"job is history and must not block a new submission", first.ID)
	}

	// Then they click Retry on the old one.
	err := d.RetryJob(first.ID, now)
	if err == nil {
		t.Fatalf("RetryJob succeeded; job %d cannot be re-armed while job %d is "+
			"active for the same kind and target, and saying it worked would "+
			"leave the operator watching a job that never re-queued", first.ID, second.ID)
	}
	assertRetryConflict(t, err, first.ID, second.ID)

	// The refused retry must have changed nothing. A partial re-arm -- attempts
	// zeroed, last_error cleared, state left terminal -- would erase the
	// evidence of why it failed in the first place.
	reread := mustGetJob(t, d, first.ID)
	if reread.State != jobs.StateFailed {
		t.Errorf("job %d = %s after a refused retry, want it left failed", first.ID, reread.State)
	}
	if reread.Error != "the disk was full" {
		t.Errorf("job %d last_error = %q after a refused retry, want the original "+
			"failure still recorded", first.ID, reread.Error)
	}

	// POSITIVE CONTROL. Everything above would also pass against a RetryJob
	// that refused every retry, which is a worse bug than the one being fixed.
	// Once the winner is out of the way the retry has to go through.
	if err := d.FinishJob(second.ID, jobs.StateDone, "", now); err != nil {
		t.Fatalf("FinishJob on the winner: %v", err)
	}
	if err := d.RetryJob(first.ID, now); err != nil {
		t.Fatalf("RetryJob once nothing is active for the target = %v; the "+
			"conflict must be about the twin, not about retrying at all", err)
	}
	if got := mustGetJob(t, d, first.ID); got.State != jobs.StateQueued {
		t.Errorf("job %d = %s, want queued", first.ID, got.State)
	}
}

// THE SECOND SHAPE, and the one most likely to be met right after the upgrade:
// a twin that MigrateJobUniqueTarget itself cancelled, sitting beside its
// survivor.
//
// The fold leaves that row carrying a last_error explaining that it was
// cancelled as a duplicate. An operator reading the jobs list sees a cancelled
// job with an explanation, and the obvious thing to do with a cancelled job is
// click Retry -- so the migration's own output is what walks the operator onto
// the defect. Built through schemaSQL rather than Open for the reason the fold
// test records: Open is what installs the index.
func TestRetryingATwinTheMigrationCancelledIsAConflictNotACrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := raw.Exec(schemaSQL); err != nil {
		raw.Close()
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO jobs
		(id, kind, target, unique_target, state, created_at, updated_at) VALUES
		(1, 'transcribe', 'recording:9', 1, 'queued', 1000, 1000),
		(2, 'transcribe', 'recording:9', 1, 'queued', 1001, 1001)`); err != nil {
		raw.Close()
		t.Fatalf("seed the pair the old race produced: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	twin := mustGetJob(t, d, 2)
	if twin.State != jobs.StateCancelled {
		t.Fatalf("job 2 = %s, want cancelled by the fold; this test is not "+
			"exercising the upgrade path it claims to", twin.State)
	}
	if twin.Error == "" {
		t.Fatal("the folded twin carries no explanation, so the click this test " +
			"is about would not be invited; the fixture is wrong")
	}

	err = d.RetryJob(2, time.Now())
	if err == nil {
		t.Fatal("RetryJob on the folded twin succeeded; job 1 is still queued " +
			"for the same kind and target")
	}
	assertRetryConflict(t, err, 2, 1)

	if got := mustGetJob(t, d, 2); got.State != jobs.StateCancelled {
		t.Errorf("job 2 = %s after a refused retry, want it left cancelled", got.State)
	}
	if got := mustGetJob(t, d, 1); got.State != jobs.StateQueued {
		t.Errorf("job 1 = %s, want the survivor untouched by a retry of its twin", got.State)
	}
}

// THE WRITERS WITH NO STATE GUARD AT ALL -- RequeueJob and RescheduleJob --
// are safe, and this is the claim their doc comments make, checked rather than
// asserted in prose.
//
// Both UPDATE by id with nothing about state in the WHERE, which looks like
// RetryJob's defect. It is not, and the difference is not the guard: it is
// where the row starts. Every caller of both reaches them with a job ClaimJob
// has already moved to 'running', and 'running' is INSIDE the index's
// predicate. A row already inside cannot collide by moving to another state
// inside -- its (kind, target) is the entry the index is holding. Only RetryJob
// selects rows from outside the predicate, which is why only RetryJob needed
// the treatment.
//
// If someone gives either of these a caller that can pass a terminal job, this
// test still passes and the doc comment becomes false. The check that would
// catch that is the writer inventory in MigrateJobUniqueTarget's comment, read
// by the next person to add a writer -- rung 0, and said out loud as such.
func TestRequeueAndRescheduleCannotCollideWithTheUniqueTargetIndex(t *testing.T) {
	d := testDB(t)
	target := jobs.RecordingTarget(9)
	now := time.Now()

	j := mustEnqueue(t, d, jobs.Job{Kind: "transcribe", Target: target, Unique: true})
	claimed, err := d.ClaimJob([]jobs.Kind{"transcribe"}, now)
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if claimed == nil || claimed.ID != j.ID {
		t.Fatalf("ClaimJob returned %v, want job %d; the rest of this test is "+
			"about a job the queue has claimed", claimed, j.ID)
	}
	if claimed.State != jobs.StateRunning {
		t.Fatalf("claimed job is %s, want running -- the premise of both doc "+
			"comments is that these two are only ever reached with a running row",
			claimed.State)
	}

	if err := d.RequeueJob(j.ID, now, "no worker is registered for this kind yet", now); err != nil {
		t.Fatalf("RequeueJob on a claimed job = %v; on the shutdown path the "+
			"alternative to requeuing is losing the work entirely", err)
	}
	if got := mustGetJob(t, d, j.ID); got.State != jobs.StateQueued {
		t.Errorf("job %d = %s after RequeueJob, want queued", j.ID, got.State)
	}

	if _, err := d.ClaimJob([]jobs.Kind{"transcribe"}, now); err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if err := d.RescheduleJob(j.ID, now.Add(time.Minute), "ffmpeg exited 1", now); err != nil {
		t.Fatalf("RescheduleJob on a claimed job = %v", err)
	}
	if got := mustGetJob(t, d, j.ID); got.State != jobs.StateQueued {
		t.Errorf("job %d = %s after RescheduleJob, want queued", j.ID, got.State)
	}
}

// assertRetryConflict is the whole contract of the answer, in one place because
// both shapes above have to produce the same one.
//
// The sentinel is what internal/api branches on, so it is what decides between
// 409 and 500 -- and matching only on the sentinel would pass for an error
// whose text was still the driver's. The text is checked too: it must NAME the
// job in the way, because "you cannot retry this" with no id leaves an operator
// with nothing to click, and it must NOT carry the index's name or the driver's
// wording, which is the 500 body this fix exists to stop shipping.
func assertRetryConflict(t *testing.T, err error, retried, active int64) {
	t.Helper()
	if !errors.Is(err, ErrStateConflict) {
		t.Fatalf("RetryJob = %v, want an error wrapping ErrStateConflict. "+
			"Anything else reaches writeStoreError's default branch and is "+
			"answered 500 with the driver's own words as the body.", err)
	}
	msg := err.Error()
	for _, want := range []string{
		fmt.Sprintf("job %d", retried),
		fmt.Sprintf("job %d is already active", active),
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("conflict text %q does not contain %q; the operator has to "+
				"be told which job is in the way", msg, want)
		}
	}
	for _, forbidden := range []string{
		"UNIQUE constraint failed", jobUniqueTargetIndex, "2067",
	} {
		if strings.Contains(msg, forbidden) {
			t.Errorf("conflict text %q leaks %q -- the whole point is that an "+
				"operator is not shown the name of an index", msg, forbidden)
		}
	}
}

// RescheduleJob AND RequeueJob CAN BE REFUSED TOO, which the writer inventory
// in jobs.go denied until a reviewer probed it instead of reading it.
//
// The inventory listed both as "reached only with a running job" and therefore
// incapable of colliding. That is a property of their CALLERS, not of them --
// neither UPDATE restricts the row's state -- and the callers have a hole.
// dispatchOnce claims a row (-> running) and only afterwards registers it in
// q.running; Queue.Cancel checks q.running, so inside that window it takes the
// !running branch and cancels a row that has a live worker on it. The target is
// then free, a resubmission is accepted, and the worker's retryable error
// arrives at finish -> RescheduleJob holding a TERMINAL row with an active twin.
//
// WHY THIS IS WORSE THAN THE RETRY CASE and not merely equal to it: finish does
// not return the error to anybody. It logs it. So without the branch under test
// the job stays cancelled for ever, the worker's outcome is discarded, and the
// operator sees nothing at all -- where the retry path at least produced a 500.
//
// The window is narrow and neither of these tests goes through it; they set the
// state up directly, because what is being pinned is the DATABASE call's
// behaviour when it is handed that state, not the odds of arriving there.
func TestReschedulingATerminalJobWhoseTargetIsNowTakenIsAConflictNotACrash(t *testing.T) {
	d := testDB(t)
	target := jobs.RecordingTarget(11)
	now := time.Now()

	first := mustEnqueue(t, d, jobs.Job{Kind: "transcribe", Target: target, Unique: true})
	if err := d.CancelJob(first.ID, now); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	second := mustEnqueue(t, d, jobs.Job{Kind: "transcribe", Target: target, Unique: true})

	err := d.RescheduleJob(first.ID, now.Add(time.Minute), "ffmpeg died", now)
	if err == nil {
		t.Fatalf("RescheduleJob succeeded; job %d cannot return to the queue while "+
			"job %d is active for the same kind and target", first.ID, second.ID)
	}
	assertUniqueTargetConflict(t, err, first.ID, second.ID)

	// POSITIVE CONTROL: with the winner finished, the reschedule must go through.
	// Without this, a RescheduleJob that refused everything would pass.
	if err := d.FinishJob(second.ID, jobs.StateDone, "", now); err != nil {
		t.Fatalf("FinishJob on the winner: %v", err)
	}
	if err := d.RescheduleJob(first.ID, now.Add(time.Minute), "ffmpeg died", now); err != nil {
		t.Fatalf("RescheduleJob with the target free: %v", err)
	}
}

func TestRequeueingATerminalJobWhoseTargetIsNowTakenIsAConflictNotACrash(t *testing.T) {
	d := testDB(t)
	target := jobs.RecordingTarget(12)
	now := time.Now()

	first := mustEnqueue(t, d, jobs.Job{Kind: "transcribe", Target: target, Unique: true})
	if err := d.CancelJob(first.ID, now); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	second := mustEnqueue(t, d, jobs.Job{Kind: "transcribe", Target: target, Unique: true})

	err := d.RequeueJob(first.ID, now.Add(time.Minute), "no processor for this kind yet", now)
	if err == nil {
		t.Fatalf("RequeueJob succeeded; job %d cannot return to the queue while "+
			"job %d is active for the same kind and target", first.ID, second.ID)
	}
	assertUniqueTargetConflict(t, err, first.ID, second.ID)

	// POSITIVE CONTROL, and it carries a second claim: RequeueJob REFUNDS the
	// attempt, and the refused call must not have spent one. A requeue that
	// both refused and charged would quietly walk the job toward its ceiling.
	before := mustGetJob(t, d, first.ID)
	if err := d.FinishJob(second.ID, jobs.StateDone, "", now); err != nil {
		t.Fatalf("FinishJob on the winner: %v", err)
	}
	if err := d.RequeueJob(first.ID, now.Add(time.Minute), "no processor yet", now); err != nil {
		t.Fatalf("RequeueJob with the target free: %v", err)
	}
	if after := mustGetJob(t, d, first.ID); after.Attempts > before.Attempts {
		t.Errorf("attempts went %d -> %d across a refused requeue and an accepted "+
			"one; the refused call must not have charged an attempt",
			before.Attempts, after.Attempts)
	}
}

// assertUniqueTargetConflict is assertRetryConflict's verb-agnostic twin. The
// two are separate because the retry one names RetryJob in its failure text,
// and a message that names the wrong function sends the next reader to the
// wrong place.
func assertUniqueTargetConflict(t *testing.T, err error, moved, active int64) {
	t.Helper()
	if !errors.Is(err, ErrStateConflict) {
		t.Fatalf("= %v, want an error wrapping ErrStateConflict. Anything else is "+
			"the raw driver error, which on this path is not shown to anybody at "+
			"all -- jobs.finish only logs it.", err)
	}
	msg := err.Error()
	for _, want := range []string{
		fmt.Sprintf("job %d", moved),
		fmt.Sprintf("job %d is already active", active),
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("conflict text %q does not contain %q", msg, want)
		}
	}
	for _, forbidden := range []string{"UNIQUE constraint failed", jobUniqueTargetIndex, "2067"} {
		if strings.Contains(msg, forbidden) {
			t.Errorf("conflict text %q leaks %q", msg, forbidden)
		}
	}
}
