package supervisor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/failreason"
)

// refusedKeyLine is what MediaMTX's refusal of a publish password looks like
// on FFmpeg's stderr (internal/failreason/testdata/rtmp-auth-failed.stderr).
const refusedKeyLine = "[rtmp @ 0xffff90a85910] Server error: authentication failed"

func quickPolicy() Spec {
	return Spec{AutoRestart: true, MinBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
}

// TestARefusedKeyIsHeldAfterThreeQuickRejectionsAndRetriedNoMore is the hold.
// Three refusals in a row, each inside the quick window, and the supervisor
// stops: StateFailed, the reason marked held, and no fourth publish against a
// platform that has already said no three times.
func TestARefusedKeyIsHeldAfterThreeQuickRejectionsAndRetriedNoMore(t *testing.T) {
	spec := quickPolicy()
	spec.HoldAfterQuickRejections = 3
	p := testProcess(t, fakeExitSaying(1, refusedKeyLine), spec)
	p.Start()
	waitFor(t, "the destination to be held", func() bool {
		st := p.Status()
		return st.State == StateFailed && st.Reason != nil && st.Reason.Held
	})
	st := p.Status()
	if st.Reason.Cause != failreason.KeyRejected || st.Reason.Rejections != 3 {
		t.Fatalf("reason = %+v, want key_rejected after 3 rejections", st.Reason)
	}
	// Three spawns are two restarts. A fourth spawn would be the retry the
	// hold exists to prevent.
	if st.Restarts != 2 {
		t.Fatalf("restarts = %d, want 2 (three spawns, then held)", st.Restarts)
	}
	time.Sleep(50 * time.Millisecond)
	if got := p.Status().Restarts; got != 2 {
		t.Fatalf("restarts moved to %d after the hold", got)
	}
	if !strings.Contains(st.LastError, "held after 3") {
		t.Fatalf("lastError = %q, want it to say the destination was held", st.LastError)
	}
}

// TestRetryLiftsTheHoldAndPublishesAgain: one click. Restart is the door, as
// it already is for a destination that gave up under MaxRestarts, and the hold
// must not survive it -- a retry that came back "held" without trying would be
// a button that does nothing.
func TestRetryLiftsTheHoldAndPublishesAgain(t *testing.T) {
	spec := quickPolicy()
	spec.HoldAfterQuickRejections = 3
	p := testProcess(t, fakeExitSaying(1, refusedKeyLine), spec)
	p.Start()
	waitFor(t, "the destination to be held", func() bool {
		r := p.Status().Reason
		return r != nil && r.Held
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.Restart(ctx)
	waitFor(t, "the retry to spawn again", func() bool { return p.Status().Restarts > 2 })
	// It is held again after three more, and that is correct: nothing changed
	// at the ingest. What matters is that it TRIED.
	waitFor(t, "the second hold", func() bool {
		r := p.Status().Reason
		return r != nil && r.Held && p.Status().State == StateFailed
	})
}

// TestNetworkFailuresKeepTheNormalBackoffAndAreNeverHeld: a refused port, a
// hostname that does not resolve and a timeout can all clear by themselves,
// and holding them would leave a destination off air after the network came
// back.
func TestNetworkFailuresKeepTheNormalBackoffAndAreNeverHeld(t *testing.T) {
	for _, line := range []string{
		"[tcp @ 0x1] Connection to tcp://ingest:1936?tcp_nodelay=0 failed: Connection refused",
		"[tcp @ 0x1] Failed to resolve hostname ingest.invalid: Name does not resolve",
		"[out#0/flv @ 0x1] Error opening output rtmp://ingest/live: Operation timed out",
	} {
		spec := quickPolicy()
		spec.HoldAfterQuickRejections = 3
		p := testProcess(t, fakeExitSaying(1, line), spec)
		p.Start()
		waitFor(t, "five retries", func() bool { return p.Status().Restarts >= 5 })
		st := p.Status()
		if st.State == StateFailed || (st.Reason != nil && st.Reason.Held) {
			t.Fatalf("%q: state=%s reason=%+v, want it still retrying", line, st.State, st.Reason)
		}
		if st.Reason == nil || !st.Reason.Cause.Network() {
			t.Fatalf("%q: reason = %+v, want a network cause", line, st.Reason)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = p.Stop(ctx)
		cancel()
	}
}

// TestAlreadyPublishingIsNeverCountedTowardTheHold: it arrives as EPERM, the
// same errno as a refused password, and it is the platform holding the
// PREVIOUS session -- which it lets go of within a minute. Holding on it would
// take a destination off air precisely when it was about to come back.
func TestAlreadyPublishingIsNeverCountedTowardTheHold(t *testing.T) {
	spec := quickPolicy()
	spec.HoldAfterQuickRejections = 3
	p := testProcess(t, fakeExitSaying(1, "[rtmp @ 0x1] Server error: Already publishing"), spec)
	p.Start()
	waitFor(t, "five retries", func() bool { return p.Status().Restarts >= 5 })
	st := p.Status()
	if st.State == StateFailed || st.Reason == nil || st.Reason.Cause != failreason.PlatformNotReady {
		t.Fatalf("state=%s reason=%+v, want platform_not_ready, still retrying", st.State, st.Reason)
	}
}

// TestNoHoldUnlessTheSpecAsksForOne: the ingest, the recorder and every other
// child keep exactly the behaviour they had. Only destinations opt in.
func TestNoHoldUnlessTheSpecAsksForOne(t *testing.T) {
	p := testProcess(t, fakeExitSaying(1, refusedKeyLine), quickPolicy())
	p.Start()
	waitFor(t, "five retries", func() bool { return p.Status().Restarts >= 5 })
	if st := p.Status(); st.State == StateFailed {
		t.Fatalf("held without HoldAfterQuickRejections: %+v", st.Reason)
	}
}

// TestTheReasonLineCarriesNoStreamKey: the line that explains a refused key is
// the line FFmpeg prints with the publish URL in it. The reason travels to a
// retained MQTT topic, a webhook, an alert and the timeline, so it is scrubbed
// with the process's own secret set before it is stored -- not at each outlet.
func TestTheReasonLineCarriesNoStreamKey(t *testing.T) {
	const key = "live_8f3a9c2e71b4"
	spec := quickPolicy()
	spec.Secrets = []string{key}
	p := testProcess(t, fakeExitSaying(1,
		"[out#0/flv @ 0x1] Error opening output rtmp://ingest.example/live/"+key+": I/O error"), spec)
	p.Start()
	waitFor(t, "a reason", func() bool { return p.Status().Reason != nil })
	r := p.Status().Reason
	if r.Cause != failreason.KeyRejected {
		t.Fatalf("cause = %q, want key_rejected", r.Cause)
	}
	b, _ := json.Marshal(p.Status())
	if strings.Contains(string(b), key) {
		t.Fatalf("the stream key reached the status JSON: %s", b)
	}
}

// TestRestartClearsTheReason: a reason belongs to the run that failed. Once an
// operator has asked for a fresh attempt, showing the old verdict beside a
// process that is connecting again would read as the new attempt's failure.
func TestRestartClearsTheReason(t *testing.T) {
	p := testProcess(t, fakeExitSaying(1, refusedKeyLine), Spec{AutoRestart: false})
	p.Start()
	waitFor(t, "the failure", func() bool { return p.Status().Reason != nil })
	p.mu.Lock()
	p.spec.Args = fakeSleep(time.Minute).args
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.Restart(ctx)
	waitFor(t, "the new run", func() bool { return p.Status().State == StateRunning })
	if r := p.Status().Reason; r != nil {
		t.Fatalf("reason = %+v after a restart, want none", r)
	}
}

// TestTheRateIsMeasuredOverTheWindowAndNotTheWholeRun is the reason this has a
// rate of its own. FFmpeg's speed= is the average since the run began, so a
// destination that delivered at 1.0x for an hour and then collapsed reads
// ~1.0x for most of another hour. The window reads the collapse.
func TestTheRateIsMeasuredOverTheWindowAndNotTheWholeRun(t *testing.T) {
	p := New(quietLog(), Spec{Name: "rate"})
	base := time.Now().Add(-time.Hour)
	// An hour at realtime...
	for i := 0; i <= 3600; i += 2 {
		p.sampleRateLocked(base.Add(time.Duration(i)*time.Second), int64(i)*1000)
	}
	// ...then twenty seconds at a tenth of it.
	end := base.Add(3600 * time.Second)
	for i := 1; i <= 20; i++ {
		p.sampleRateLocked(end.Add(time.Duration(i)*time.Second), 3600_000+int64(i)*100)
	}
	rate, ok := p.rateLocked(end.Add(20 * time.Second))
	if !ok || rate > 0.15 || rate < 0.05 {
		t.Fatalf("rate = %v (ok=%v), want about 0.1", rate, ok)
	}
}

// TestTheRateIsUnknownUntilThereIsAWindowToMeasure: two samples a second apart
// are not a rate, and a destination must not be called slow in the first
// seconds of a run.
func TestTheRateIsUnknownUntilThereIsAWindowToMeasure(t *testing.T) {
	p := New(quietLog(), Spec{Name: "rate"})
	now := time.Now()
	p.sampleRateLocked(now, 100)
	p.sampleRateLocked(now.Add(time.Second), 200)
	if _, ok := p.rateLocked(now.Add(time.Second)); ok {
		t.Fatal("a rate was reported from one second of history")
	}
}
