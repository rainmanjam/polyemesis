package api

import (
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// The jobs control surface is fourteen handlers, and every one of them either
// destroys something or changes what the machine will do next. These tests are
// about WHAT they do, which for a delete or a purge means the store is
// re-read afterwards: a handler that answers 200 and did nothing is
// indistinguishable from a working one if the assertion stops at the status.
//
// Principal: a SESSION throughout, via sourceServer's sign. Every route below
// is denied to a read-scoped token by requireScope, which answers 403 before
// the handler is entered -- a test driven with the wrong principal would pass
// while exercising nothing at all.
//
// Fixture: playlistJobServer, which is sourceServer with a real jobs.Queue
// attached and no Run loop. Nothing here needs a worker; submission, deletion
// and state transitions are all synchronous store writes.

// enqueued puts one job in the queue's store and returns it.
func enqueued(t *testing.T, store *db.DB, kind jobs.Kind, target string) *jobs.Job {
	t.Helper()
	j, err := store.EnqueueJob(jobs.Job{Kind: kind, Target: target})
	if err != nil {
		t.Fatalf("EnqueueJob(%s): %v", kind, err)
	}
	return j
}

// finishedAgo enqueues a job and marks it done at an EXPLICIT instant.
//
// The timestamp is a parameter of FinishJob rather than a wall-clock wait,
// which is what lets a retention test span days without a sleep.
func finishedAgo(t *testing.T, store *db.DB, kind jobs.Kind, ago time.Duration) *jobs.Job {
	t.Helper()
	j := enqueued(t, store, kind, "")
	if err := store.FinishJob(j.ID, jobs.StateDone, "", time.Now().Add(-ago)); err != nil {
		t.Fatalf("FinishJob(%d): %v", j.ID, err)
	}
	return j
}

// jobsFixture is playlistJobServer plus the store, which every assertion here
// needs in order to re-read what the handler claims to have done.
func jobsFixture(t *testing.T) (http.Handler, func(*http.Request), *db.DB) {
	t.Helper()
	h, sign, srv, _ := playlistJobServer(t)
	return h, sign, srv.store
}

func jobPath(id int64) string {
	return "/api/v1/jobs/" + strconv.FormatInt(id, 10)
}

// TestDeletingOneJobRemovesThatRowAndNoOther is the API half of the scoping
// claim internal/db's TestDeleteJobRemovesOnlyThatRow owns.
//
// The response SHAPE is the deletion oracle here, and it is worth spelling out
// why an exact match is asserted rather than "200". jobAction re-reads the job
// after acting: a delete that really happened makes that read fail and the body
// is {"status":"ok"}, while a delete that silently did nothing makes the read
// SUCCEED and the body becomes a full jobView. Same status either way.
func TestDeletingOneJobRemovesThatRowAndNoOther(t *testing.T) {
	h, sign, store := jobsFixture(t)

	target := enqueued(t, store, "target", jobs.RecordingTarget(1))
	witness := enqueued(t, store, "witness", jobs.RecordingTarget(2))

	var body map[string]any
	decodeInto(t, send(t, h, sign, http.MethodDelete,
		jobPath(target.ID), nil, http.StatusOK), &body)

	if len(body) != 1 || body["status"] != "ok" {
		t.Errorf(`DELETE answered %v, want exactly {"status":"ok"}; a jobView here `+
			`means jobAction could still read the job back, so nothing was deleted`, body)
	}
	if _, err := store.GetJob(target.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("job %d still loads after DELETE answered 200: %v", target.ID, err)
	}
	if _, err := store.GetJob(witness.ID); err != nil {
		t.Fatalf("witness job %d was deleted too: DELETE of one job took another with it (%v)",
			witness.ID, err)
	}
	// Deleting it again is a 404 rather than a second 200, which is what says
	// the first delete was real and not idempotent-by-doing-nothing.
	msg := mustJSONError(t, h, sign, http.MethodDelete, jobPath(target.ID), nil, http.StatusNotFound)
	if msg == "" {
		t.Error("the second DELETE carried no sentence")
	}
}

// TestPurgeAppliesTheDaysAndKeepItWasGiven watches the two numbers in the
// request body arrive at the store as a cutoff and a floor.
//
// The stored RetainJobs is set to zero FIRST and that is load-bearing: the
// shipped default is 200, which shields every row a unit test could seed, and
// with it in place a purge whose cutoff was ignored entirely still purges
// nothing and still looks correct.
func TestPurgeAppliesTheDaysAndKeepItWasGiven(t *testing.T) {
	h, sign, store := jobsFixture(t)

	if _, err := store.UpdateSettings(func(s *db.Settings) error {
		s.PostProd.RetainJobs = 0
		return nil
	}); err != nil {
		t.Fatalf("clear the retention floor: %v", err)
	}

	old := finishedAgo(t, store, "old", 96*time.Hour)
	recent := finishedAgo(t, store, "recent", 72*time.Hour)

	var out struct {
		Purged int `json:"purged"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/jobs/purge",
		map[string]int{"days": 2, "keep": 1}, http.StatusOK), &out)

	if out.Purged != 1 {
		t.Errorf("purged %d, want 1: two rows are older than the two-day cutoff and "+
			"keep=1 spares the newer of them", out.Purged)
	}
	if _, err := store.GetJob(old.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("the 96h-old job %d survived a purge with days=2 keep=1: %v", old.ID, err)
	}
	if _, err := store.GetJob(recent.ID); err != nil {
		t.Errorf("the 72h-old job %d was purged, but keep=1 must spare the newest "+
			"finished job whatever its age (%v)", recent.ID, err)
	}
}

// TestPurgeWithZeroDaysKeepsHistoryForever pins the sentinel.
//
// Zero days is "keep forever", expressed to a cutoff-based purge as a cutoff
// nothing can be older than. The guard that produces it is `if days > 0`, and
// widening it by one character to `>=` turns the never-purge request into a
// purge-everything one -- which is the single worst outcome this endpoint has.
func TestPurgeWithZeroDaysKeepsHistoryForever(t *testing.T) {
	h, sign, store := jobsFixture(t)

	ancient := finishedAgo(t, store, "ancient", 400*24*time.Hour)

	var out struct {
		Purged int `json:"purged"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/jobs/purge",
		map[string]int{"days": 0, "keep": 0}, http.StatusOK), &out)

	if out.Purged != 0 {
		t.Errorf("purged %d with days=0, want 0: zero days means keep forever", out.Purged)
	}
	if _, err := store.GetJob(ancient.ID); err != nil {
		t.Errorf("a 400-day-old job was destroyed by a request that asked for nothing "+
			"to be purged (%v)", err)
	}
}

// TestPurgeBodySemantics covers the three shapes of body the endpoint can be
// sent, because they are three different behaviours and only one of them is
// "purge with these numbers".
func TestPurgeBodySemantics(t *testing.T) {
	t.Run("an empty body is refused and purges nothing", func(t *testing.T) {
		h, sign, store := jobsFixture(t)
		survivor := finishedAgo(t, store, "witness", 400*24*time.Hour)

		// decodeJSON is unconditional: there is no ContentLength check in front
		// of it, so a body-less POST is a decode error and not a defaults run.
		msg := mustJSONError(t, h, sign, http.MethodPost, "/api/v1/jobs/purge",
			nil, http.StatusBadRequest)
		if msg != "invalid request body: EOF" {
			t.Errorf("empty body answered %q, want %q", msg, "invalid request body: EOF")
		}
		if _, err := store.GetJob(survivor.ID); err != nil {
			t.Errorf("a refused purge deleted a job anyway (%v)", err)
		}
	})

	t.Run("an empty object purges by the STORED retention", func(t *testing.T) {
		h, sign, store := jobsFixture(t)

		// A retention nothing could have hardcoded, written to the store and
		// then read BACK from it. The shipped defaults are 30 days and a floor
		// of 200, so a handler with those two numbers baked in would agree with
		// a test that asserted them -- which is the whole failure mode this
		// subtest exists to catch.
		if _, err := store.UpdateSettings(func(s *db.Settings) error {
			s.PostProd.RetainDays = 7
			s.PostProd.RetainJobs = 2
			return nil
		}); err != nil {
			t.Fatalf("store a distinctive retention: %v", err)
		}
		settings, err := store.GetSettings()
		if err != nil {
			t.Fatalf("GetSettings: %v", err)
		}
		days := settings.PostProd.RetainDays
		keep := settings.PostProd.RetainJobs

		// `keep` rows INSIDE the window, which consume the floor entirely, plus
		// one outside it that therefore has nothing left to shelter it. Exactly
		// one row must go, and it is the one whose age -- not its position in
		// the ordering -- put it there.
		fresh := make([]*jobs.Job, 0, keep)
		for i := 0; i < keep; i++ {
			fresh = append(fresh, finishedAgo(t, store, "fresh",
				time.Duration(days*24-24-i)*time.Hour))
		}
		aged := finishedAgo(t, store, "aged", time.Duration(days*24+48)*time.Hour)

		var out struct {
			Purged int `json:"purged"`
		}
		decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/jobs/purge",
			map[string]any{}, http.StatusOK), &out)

		if out.Purged != 1 {
			t.Errorf("purged %d with {}, want 1: one row is older than the stored "+
				"%d-day cutoff and the stored floor of %d is spent on newer ones",
				out.Purged, days, keep)
		}
		if _, err := store.GetJob(aged.ID); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("job %d finished outside the stored %d-day window and survived a "+
				"defaulted purge: %v", aged.ID, days, err)
		}
		for _, j := range fresh {
			if _, err := store.GetJob(j.ID); err != nil {
				t.Errorf("job %d finished inside the stored %d-day window and was purged (%v)",
					j.ID, days, err)
			}
		}
	})

	t.Run("a negative day count is refused", func(t *testing.T) {
		h, sign, store := jobsFixture(t)
		// No witness row on purpose. days=-1 fails the `days > 0` guard, so the
		// cutoff would stay zero and nothing would be purged even without the
		// validation -- a survivor here would prove nothing about the 400.
		msg := mustJSONError(t, h, sign, http.MethodPost, "/api/v1/jobs/purge",
			map[string]int{"days": -1}, http.StatusBadRequest)
		if msg != "retention values cannot be negative" {
			t.Errorf("days=-1 answered %q, want %q", msg, "retention values cannot be negative")
		}
		if _, err := store.GetSettings(); err != nil {
			t.Fatalf("GetSettings: %v", err)
		}
	})
}

// TestPauseAndResumeAreVisibleOnTheOverview reads the effect somewhere other
// than the handler that caused it.
//
// handlePauseJobs answers a literal {"paused":true} -- it is written into the
// map, not read from the queue -- so asserting on its own body would pass with
// the Pause() call deleted. The overview asks the QUEUE.
func TestPauseAndResumeAreVisibleOnTheOverview(t *testing.T) {
	h, sign, _ := jobsFixture(t)

	paused := func() bool {
		t.Helper()
		var out struct {
			Available bool `json:"available"`
			Paused    bool `json:"paused"`
		}
		decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/jobs/overview",
			nil, http.StatusOK), &out)
		if !out.Available {
			t.Fatal("the overview reports no queue; this fixture wired one")
		}
		return out.Paused
	}

	if paused() {
		t.Fatal("a fresh queue is already paused")
	}
	send(t, h, sign, http.MethodPost, "/api/v1/jobs/pause", nil, http.StatusOK)
	if !paused() {
		t.Error("POST /jobs/pause answered 200 and the queue is still running: " +
			"the overview reports paused=false")
	}
	send(t, h, sign, http.MethodPost, "/api/v1/jobs/resume", nil, http.StatusOK)
	if paused() {
		t.Error("POST /jobs/resume answered 200 and the queue is still paused")
	}
}

// TestCancelAndRetryMoveTheJobAndTheStoreAgrees asserts the transition twice:
// once in the response jobAction built, and once by re-reading the row.
//
// One without the other is not enough. The response alone would pass if the
// view were assembled from pre-action state; the store alone would pass if the
// handler returned something unrelated to what it did.
func TestCancelAndRetryMoveTheJobAndTheStoreAgrees(t *testing.T) {
	h, sign, store := jobsFixture(t)
	j := enqueued(t, store, "movable", jobs.RecordingTarget(7))

	var cancelled struct {
		State      string    `json:"state"`
		FinishedAt time.Time `json:"finishedAt"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost,
		jobPath(j.ID)+"/cancel", nil, http.StatusOK), &cancelled)

	if cancelled.State != string(jobs.StateCancelled) {
		t.Errorf("cancel answered state %q, want %q", cancelled.State, jobs.StateCancelled)
	}
	if cancelled.FinishedAt.IsZero() {
		t.Error("a cancelled job carries no finishedAt; the operator cannot tell when it stopped")
	}
	stored, err := store.GetJob(j.ID)
	if err != nil {
		t.Fatalf("GetJob after cancel: %v", err)
	}
	if stored.State != jobs.StateCancelled {
		t.Errorf("the store still holds job %d in state %q after a cancel that answered "+
			"%q: the response was built from state the action never wrote",
			j.ID, stored.State, cancelled.State)
	}

	var retried struct {
		State    string `json:"state"`
		Attempts int    `json:"attempts"`
		Error    string `json:"error"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost,
		jobPath(j.ID)+"/retry", nil, http.StatusOK), &retried)

	if retried.State != string(jobs.StateQueued) {
		t.Errorf("retry answered state %q, want %q", retried.State, jobs.StateQueued)
	}
	if retried.Attempts != 0 {
		t.Errorf("retry left attempts at %d; a retry is the operator saying "+
			"\"try again from the start\", not resuming against a spent budget", retried.Attempts)
	}
	if retried.Error != "" {
		t.Errorf("retry left the last error %q in place", retried.Error)
	}
	stored, err = store.GetJob(j.ID)
	if err != nil {
		t.Fatalf("GetJob after retry: %v", err)
	}
	if stored.State != jobs.StateQueued {
		t.Errorf("the store still holds job %d in state %q after a retry that answered %q",
			j.ID, stored.State, retried.State)
	}
	if !stored.FinishedAt.IsZero() {
		t.Errorf("the re-queued job %d still carries a finishedAt", j.ID)
	}
}

// TestJobsRoutesNeedAQueueExceptThePolicyAndTheOverview pins the degraded-mode
// contract, which is a real one and not a formality: the jobs page must still
// render AND still be configurable on a build with no queue wired, and it can
// only do that because three of these routes answer 200 rather than 503.
//
// A refactor that "added the missing guard for consistency" would break exactly
// those three and nothing else would notice.
//
// Measured correction to the plan this was written from: the exceptions are
// THREE, not two. PUT /jobs/policy carries no requireJobs either -- the policy
// is stored settings, and an operator must be able to set the concurrency a
// queue will use before the build that has one is running. It is listed with
// the other exceptions rather than with the 503s.
//
// The fixture is sourceServer with jobq left nil -- deliberately NOT testServer,
// which has no engine and panics inside whisperInfo on GET /jobs/overview.
func TestJobsRoutesNeedAQueueExceptThePolicyAndTheOverview(t *testing.T) {
	h, _, sign := sourceServer(t)
	srv := serverUnderTest(t, h)
	if srv.jobq != nil {
		t.Fatal("this fixture is supposed to have no queue")
	}

	needQueue := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/jobs"},
		{http.MethodPost, "/api/v1/jobs/pause"},
		{http.MethodPost, "/api/v1/jobs/resume"},
		{http.MethodPost, "/api/v1/jobs/purge"},
		{http.MethodGet, "/api/v1/jobs/1"},
		{http.MethodDelete, "/api/v1/jobs/1"},
		{http.MethodPost, "/api/v1/jobs/1/cancel"},
		{http.MethodPost, "/api/v1/jobs/1/retry"},
		{http.MethodPost, "/api/v1/jobs/1/release"},
	}
	for _, tc := range needQueue {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			msg := mustJSONError(t, h, sign, tc.method, tc.path, map[string]any{},
				http.StatusServiceUnavailable)
			// The sentence is spelled out rather than compared against
			// jobsUnavailable. Comparing a constant with itself is a tautology
			// -- it moves with any edit to the constant -- and this string is a
			// contract: it is what the UI shows an operator whose build has no
			// queue, and what tells them the route exists and the capability
			// might come back rather than that their server is too old.
			const want = "the background job queue is not running on this server"
			if msg != want {
				t.Errorf("said %q, want %q -- one sentence for the whole surface, so "+
					"the UI can recognise it", msg, want)
			}
		})
	}

	t.Run("the overview still renders", func(t *testing.T) {
		var out struct {
			Available bool          `json:"available"`
			Kinds     []jobKindInfo `json:"kinds"`
		}
		decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/jobs/overview",
			nil, http.StatusOK), &out)
		if out.Available {
			t.Error("the overview claims a queue is available on a server with none")
		}
		if len(out.Kinds) == 0 {
			t.Error("the overview carries no kind catalogue, so the page has nothing " +
				"to explain the absent queue with")
		}
	})

	t.Run("the policy is still readable and writable", func(t *testing.T) {
		var out struct {
			Policy db.PostProdSettings `json:"policy"`
			Modes  []string            `json:"modes"`
		}
		decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/jobs/policy",
			nil, http.StatusOK), &out)
		if out.Policy.Concurrency == 0 || len(out.Modes) == 0 {
			t.Errorf("GET /jobs/policy answered 200 with nothing in it: %+v", out)
		}

		policy := out.Policy
		policy.RetainDays = out.Policy.RetainDays + 3
		var put struct {
			Policy db.PostProdSettings `json:"policy"`
		}
		decodeInto(t, send(t, h, sign, http.MethodPut, "/api/v1/jobs/policy",
			policy, http.StatusOK), &put)
		if put.Policy.RetainDays != policy.RetainDays {
			t.Errorf("PUT /jobs/policy on a queueless server answered retainDays %d, want %d",
				put.Policy.RetainDays, policy.RetainDays)
		}
		decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/jobs/policy",
			nil, http.StatusOK), &out)
		if out.Policy.RetainDays != policy.RetainDays {
			t.Errorf("the policy written without a queue did not survive a re-read: "+
				"got %d, want %d", out.Policy.RetainDays, policy.RetainDays)
		}
	})
}

// TestPuttingTheJobPolicyLeavesTheRestOfTheSettingsAlone is about the comment
// on handlePutJobPolicy: the post-production block is a SLICE of the one
// settings document, so it is written inside UpdateSettings rather than by a
// read-modify-write of its own. A refactor to the obvious-looking
// GetSettings/SaveSettings pair would discard whatever any other writer had
// just stored.
func TestPuttingTheJobPolicyLeavesTheRestOfTheSettingsAlone(t *testing.T) {
	h, sign, store := jobsFixture(t)

	before, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	// A sibling block with a value nothing about the jobs policy could produce,
	// so its survival is evidence rather than coincidence.
	if _, err := store.UpdateSettings(func(s *db.Settings) error {
		s.Recording.MinFreeGB = 42
		return nil
	}); err != nil {
		t.Fatalf("seed the sibling block: %v", err)
	}
	before, err = store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}

	policy := before.PostProd
	policy.Concurrency = before.PostProd.Concurrency + 1
	policy.RetainDays = before.PostProd.RetainDays + 5

	var out struct {
		Policy          db.PostProdSettings `json:"policy"`
		RestartRequired bool                `json:"restartRequired"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPut, "/api/v1/jobs/policy",
		policy, http.StatusOK), &out)

	after, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if after.PostProd.Concurrency != policy.Concurrency {
		t.Errorf("stored concurrency %d, want %d", after.PostProd.Concurrency, policy.Concurrency)
	}
	if after.PostProd.RetainDays != policy.RetainDays {
		t.Errorf("stored retainDays %d, want %d", after.PostProd.RetainDays, policy.RetainDays)
	}
	// reflect.DeepEqual rather than `!=`, which needs db.RecordingSettings to
	// stay comparable. It is all scalars today, so `!=` compiled -- and the day
	// someone adds a slice or a map to it, a JOBS POLICY test stops compiling
	// for a reason nobody will connect to the change they made. A tripwire in an
	// unrelated file is worse than a slower comparison.
	if !reflect.DeepEqual(after.Recording, before.Recording) {
		t.Errorf("the recording settings changed when only the jobs policy was written:\n"+
			" before %+v\n  after %+v", before.Recording, after.Recording)
	}
	// Computed from the observed before-value rather than asserted as a
	// constant: true only because concurrency moved.
	if want := policy.Concurrency != before.PostProd.Concurrency; out.RestartRequired != want {
		t.Errorf("restartRequired = %v, want %v (concurrency %d -> %d)",
			out.RestartRequired, want, before.PostProd.Concurrency, policy.Concurrency)
	}

	// A second PUT that leaves concurrency alone must NOT ask for a restart:
	// the flag is about the one field the running queue cannot pick up.
	policy.RetainDays += 5
	decodeInto(t, send(t, h, sign, http.MethodPut, "/api/v1/jobs/policy",
		policy, http.StatusOK), &out)
	if out.RestartRequired {
		t.Error("restartRequired is true for a change that did not touch concurrency; " +
			"an operator told to restart for nothing stops believing the message")
	}
}
