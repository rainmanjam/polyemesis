package engine

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/failreason"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// TestASlowUploadIsNamedWhileTheDestinationIsStillRunning: a throttled upload
// does not exit -- the capture ran a minute at 0.015x and printed nothing -- so
// a reason that waited for an exit would never name the cause an operator most
// needs to hear about.
func TestASlowUploadIsNamedWhileTheDestinationIsStillRunning(t *testing.T) {
	p := &supervisor.Status{State: supervisor.StateRunning, Rate: 0.4, RateKnown: true}
	r := destinationReason(p, true, time.Minute, time.Now())
	if r == nil || r.Cause != failreason.UploadTooSlow {
		t.Fatalf("reason = %+v, want upload_too_slow", r)
	}
}

// TestALostIngestIsNotASlowUpload: with the source gone every destination's
// rate falls together. That is one ingest event; naming a slow upload on every
// card would send the operator to their uplink for a camera that was
// unplugged. The source must have been arriving for a whole rate window.
func TestALostIngestIsNotASlowUpload(t *testing.T) {
	p := &supervisor.Status{State: supervisor.StateRunning, Rate: 0.4, RateKnown: true}
	if r := destinationReason(p, true, supervisor.RateWindow/2, time.Now()); r != nil {
		t.Fatalf("reason = %+v with the source back for half a window, want none", r)
	}
	if r := destinationReason(p, true, 0, time.Now()); r != nil {
		t.Fatalf("reason = %+v with no source, want none", r)
	}
}

// TestAStallIsNotASlowUpload: a rate of zero is the stall destinationStalled
// already reports, with its own grace. Crawling is slow; stopped is not.
func TestAStallIsNotASlowUpload(t *testing.T) {
	p := &supervisor.Status{State: supervisor.StateRunning, Rate: 0, RateKnown: true}
	if r := destinationReason(p, true, time.Minute, time.Now()); r != nil {
		t.Fatalf("reason = %+v for a stall, want none", r)
	}
}

// TestTheReasonOfAFailedRunIsACopy: DestStatus is handed to MQTT, hooks and
// the API, and the engine sets KeyChanged on it. Writing through to the
// supervisor's own value would race every other reader of that status.
func TestTheReasonOfAFailedRunIsACopy(t *testing.T) {
	orig := &failreason.Reason{Cause: failreason.KeyRejected}
	p := &supervisor.Status{State: supervisor.StateReconnecting, Reason: orig}
	r := destinationReason(p, true, time.Minute, time.Now())
	r.KeyChanged = true
	if orig.KeyChanged {
		t.Fatal("destinationReason returned the supervisor's own Reason, not a copy")
	}
}

// stubKeyChecker answers after release is closed, and counts calls.
type stubKeyChecker struct {
	calls    atomic.Int32
	inflight atomic.Int32
	maxSeen  atomic.Int32
	release  chan struct{}
	changed  bool
}

func (s *stubKeyChecker) StreamKeyChanged(ctx context.Context, destID int64) (bool, bool, error) {
	s.calls.Add(1)
	n := s.inflight.Add(1)
	defer s.inflight.Add(-1)
	for {
		m := s.maxSeen.Load()
		if n <= m || s.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	<-s.release
	return s.changed, true, nil
}

func keyRow(key string) *db.Destination {
	acct := int64(5)
	return &db.Destination{ID: 9, Name: "YT", Platform: db.PlatformYouTube, StreamKey: key, AccountID: &acct}
}

// TestTheKeyCrossCheckNeverBlocksStatusAndAsksOnceAtATime: keyChanged is called
// from Status, per WebSocket push. It must return at once with what it knows,
// and a destination refused every second must not become a platform API call
// every second -- one check at a time, then one per keyCheckEvery.
func TestTheKeyCrossCheckNeverBlocksStatusAndAsksOnceAtATime(t *testing.T) {
	k := &stubKeyChecker{release: make(chan struct{}), changed: true}
	e := &Engine{log: testLogger()}
	e.SetKeyChecker(k)
	row := keyRow("old-key")

	for i := 0; i < 50; i++ {
		if e.keyChanged(row) {
			t.Fatal("reported a changed key before any answer arrived")
		}
	}
	close(k.release)
	waitUntil(t, func() bool { return e.keyChanged(row) }, "the cross-check's answer")
	if got := k.calls.Load(); got != 1 {
		t.Fatalf("the platform was asked %d times for one refusal burst, want 1", got)
	}
	if k.maxSeen.Load() > 1 {
		t.Fatal("two checks for one destination ran at once")
	}
}

// TestANewKeyIsANewQuestion: the operator pressed Refresh stream key. The
// cached "changed" was about the OLD key, and carrying it onto the new one
// would keep telling them to click a button they have already clicked.
func TestANewKeyIsANewQuestion(t *testing.T) {
	k := &stubKeyChecker{release: make(chan struct{}), changed: true}
	close(k.release)
	e := &Engine{log: testLogger()}
	e.SetKeyChecker(k)
	waitUntil(t, func() bool { return e.keyChanged(keyRow("old-key")) }, "the first answer")
	if e.keyChanged(keyRow("new-key")) {
		t.Fatal("the answer about the old key was reported for the new one")
	}
}

// TestNoCrossCheckWithoutAConnectedAccount: a pasted key has no account to ask,
// and asking anyway would be a lookup that can only fail.
func TestNoCrossCheckWithoutAConnectedAccount(t *testing.T) {
	k := &stubKeyChecker{release: make(chan struct{})}
	close(k.release)
	e := &Engine{log: testLogger()}
	e.SetKeyChecker(k)
	row := keyRow("k")
	row.AccountID = nil
	e.keyChanged(row)
	time.Sleep(20 * time.Millisecond)
	if k.calls.Load() != 0 {
		t.Fatal("a destination with no connected account was cross-checked")
	}
}

// TestAHeldDestinationIsNotRevivedByAMoreForgivingPolicy: a settings save
// touches every destination. retunePolicy revives a destination that gave up
// under a stricter limit -- correct for that -- but a HELD destination did not
// run out of retries, it was refused, and saving an unrelated setting must not
// lift a hold the operator never asked to lift.
func TestAHeldDestinationIsNotRevivedByAMoreForgivingPolicy(t *testing.T) {
	t.Setenv(engineFakeChildEchoLast, "[rtmp @ 0x1] Server error: authentication failed for ")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	proc := supervisor.New(testLogger(), supervisor.Spec{
		Name: destSubName(7, ""), Bin: self, Args: []string{"rtmp://ingest.example/live/k"},
		AutoRestart: true, MinBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
		MaxRestarts:              2,
		HoldAfterQuickRejections: destHoldAfterQuickRejections,
	})
	proc.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = proc.Stop(ctx)
	})
	waitUntil(t, func() bool {
		r := proc.Status().Reason
		return proc.Status().State == supervisor.StateFailed && r != nil && r.Held
	}, "the destination to be held")
	heldAt := proc.Status().Restarts

	e := &Engine{log: testLogger()}
	row := backupRow()
	row.Resilience = db.DestResilience{MinBackoffSeconds: 1, MaxBackoffSeconds: 1, GiveUpAfter: 50}
	e.retunePolicy(proc, row, destPolicy(row), "")

	time.Sleep(100 * time.Millisecond)
	if got := proc.Status().Restarts; got != heldAt {
		t.Fatalf("restarts went %d -> %d: a policy change lifted the hold", heldAt, got)
	}
}
