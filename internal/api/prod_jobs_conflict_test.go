package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// Two defects in the single-job control surface, both found while writing the
// #219 tests and both deliberately left unpinned there because pinning them
// would have frozen the wrong answer: #221 (a state conflict answered 500) and
// #220 (DELETE of a running job removed the row and left its worker running).
//
// Principal: a SESSION throughout, via sourceServer's sign, for the reason
// jobs_control_test.go records -- every route under /api/v1/jobs is denied to a
// read-scoped token by requireScope, which answers 403 BEFORE the handler is
// entered. A test driven with the wrong principal would pass having exercised
// nothing. Each test below proves its principal reaches the handler before it
// asserts anything, with a 200 on the same job through the same mount.
//
// Fixture: jobsFixture (playlistJobServer), a real jobs.Queue over a real store
// with NO Run loop. That is what makes a running job cheap and deterministic
// here: the row is put in 'running' by claiming it in the STORE, so the state
// the handler reads is the state a real worker would have left, without a
// worker existing to race the assertion.

// claimed enqueues a job and takes it through ClaimJob, leaving the row in
// 'running' exactly as the queue's own claim does.
//
// ClaimJob is used rather than a hand-written UPDATE so the row carries
// everything a claim carries -- attempts, started_at -- and cannot drift from
// what a running job actually looks like.
func claimed(t *testing.T, store *db.DB, kind jobs.Kind) *jobs.Job {
	t.Helper()
	j := enqueued(t, store, kind, "")
	got, err := store.ClaimJob([]jobs.Kind{kind}, time.Now())
	if err != nil {
		t.Fatalf("ClaimJob(%s): %v", kind, err)
	}
	if got == nil {
		t.Fatalf("ClaimJob(%s) claimed nothing; the fixture has no running job to test with", kind)
	}
	if got.ID != j.ID || got.State != jobs.StateRunning {
		t.Fatalf("ClaimJob took job %d in state %q, want job %d running", got.ID, got.State, j.ID)
	}
	return got
}

// reachesTheHandler fails unless a session GET of one job answers 200.
//
// This is the guard against the whole file being vacuous: 403 from requireScope
// and 200 from a handler are both "the request completed", and only one of them
// means the assertions below touched any product code.
func reachesTheHandler(t *testing.T, h http.Handler, sign func(*http.Request), id int64) {
	t.Helper()
	var view struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, jobPath(id), nil, http.StatusOK), &view)
	if view.ID != id {
		t.Fatalf("GET %s answered a view of job %d; this principal is not reaching the "+
			"jobs handlers and nothing below would be tested", jobPath(id), view.ID)
	}
}

// TestRetryOfAQueuedJobIsAConflictNotAServerError is #221.
//
// RetryJob's UPDATE is scoped `WHERE ... state IN ('done','failed','cancelled')`,
// so retrying a QUEUED job matches zero rows and returns jobStateConflict. That
// error reached writeStoreError unclassified and fell to the default arm: 500.
//
// 500 is a claim about the SERVER. The request was merely inapplicable, and the
// difference matters to whoever is reading the logs at the time: a 500 sends
// someone looking for a broken store that is working perfectly.
//
// The status is asserted alongside the sentence, because a 409 carrying "internal
// error" would be no better than the 500 for the operator holding it.
func TestRetryOfAQueuedJobIsAConflictNotAServerError(t *testing.T) {
	h, sign, store := jobsFixture(t)

	j := enqueued(t, store, "retryable", jobs.RecordingTarget(11))
	reachesTheHandler(t, h, sign, j.ID)

	msg := mustJSONError(t, h, sign, http.MethodPost, jobPath(j.ID)+"/retry",
		nil, http.StatusConflict)
	if !strings.Contains(msg, "queued") {
		t.Errorf("retrying a queued job answered %q; the sentence has to name the state "+
			"that got in the way, or 409 is just a number", msg)
	}

	// A refusal that also changed something would be the worse bug of the two.
	stored, err := store.GetJob(j.ID)
	if err != nil {
		t.Fatalf("GetJob after the refused retry: %v", err)
	}
	if stored.State != jobs.StateQueued {
		t.Errorf("the refused retry left job %d in state %q, want %q",
			j.ID, stored.State, jobs.StateQueued)
	}

	// The same shape from the other direction: cancel of an already terminal
	// job. Both reach jobStateConflict, and one arm covering both is what says
	// the mapping is on the CLASS of error rather than on RetryJob.
	if err := store.FinishJob(j.ID, jobs.StateCancelled, "", time.Now()); err != nil {
		t.Fatalf("FinishJob: %v", err)
	}
	msg = mustJSONError(t, h, sign, http.MethodPost, jobPath(j.ID)+"/cancel",
		nil, http.StatusConflict)
	if !strings.Contains(msg, "cancelled") {
		t.Errorf("cancelling a cancelled job answered %q, which does not name its state", msg)
	}

	// And the 404 has not been swallowed by the new arm: a missing row is a
	// different answer from a wrong state, and jobStateConflict returns
	// ErrNotFound for it.
	mustJSONError(t, h, sign, http.MethodPost, jobPath(j.ID+9999)+"/retry",
		nil, http.StatusNotFound)
}

// TestDeletingARunningJobIsRefusedRatherThanOrphaningItsWorker is #220.
//
// handleDeleteJob called Queue.Delete, which is an unconditional
// `DELETE FROM jobs WHERE id = ?`. Deleting the row of a RUNNING job does not
// touch the execution: the worker keeps its context and keeps working, and the
// operator's only route to it -- the row -- is gone.
//
// The oracle is the STORE, re-read after the refusal. A 409 alone would pass
// with the row deleted anyway if the refusal were written after the delete.
func TestDeletingARunningJobIsRefusedRatherThanOrphaningItsWorker(t *testing.T) {
	h, sign, store := jobsFixture(t)

	running := claimed(t, store, "long")
	reachesTheHandler(t, h, sign, running.ID)

	msg := mustJSONError(t, h, sign, http.MethodDelete, jobPath(running.ID),
		nil, http.StatusConflict)
	if !strings.Contains(msg, "cancel") {
		t.Errorf("the refusal reads %q; it has to name the remedy, because "+
			"\"no\" without \"cancel it first\" leaves the operator with a job "+
			"they cannot remove and no instruction", msg)
	}

	stored, err := store.GetJob(running.ID)
	if err != nil {
		t.Fatalf("job %d was DELETED by a request that answered 409: its worker is "+
			"still running and its only record is gone (%v)", running.ID, err)
	}
	if stored.State != jobs.StateRunning {
		t.Errorf("the refused DELETE moved job %d to %q; refusing is not the same as "+
			"quietly cancelling, and Queue.Delete's contract says the cancel is the "+
			"caller's call", running.ID, stored.State)
	}

	// The remedy the sentence names has to actually work, or the refusal is a
	// dead end: cancel, then delete.
	send(t, h, sign, http.MethodPost, jobPath(running.ID)+"/cancel", nil, http.StatusOK)
	send(t, h, sign, http.MethodDelete, jobPath(running.ID), nil, http.StatusOK)
	if _, err := store.GetJob(running.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("job %d survived the DELETE that followed a successful cancel: %v",
			running.ID, err)
	}
}
