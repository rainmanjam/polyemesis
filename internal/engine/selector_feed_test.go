package engine

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// These tests are about the gap between DECIDING a source and RUNNING one.
//
// Task 4 taught the selector to rank a fourth kind. It did not teach the feed
// layer to build one, and it did not have to -- playoutRunning is never true
// yet. What made that dangerous is how the feed layer USED to treat a kind it
// did not recognise: feedUpstreamSig hashed it as the primary, startFeed built
// the primary's command line for it, and downstreamFeedInput handed it the
// primary's hub. All three by falling through a default, none of them saying a
// word.
//
// So the failure a later task was one missed case away from shipping is: the
// sweep decides "playout", sel.active records "playout", Failover.Reason tells
// the operator the playlist is on air -- and the process that is actually
// running is reading the primary's bytes. No error, no panic, no test failure,
// and the selector's panic recovery never fires because nothing panicked. That
// is strictly worse than a crash, because it is a broadcast going out under a
// label that is a lie.
//
// A comment cannot fail CI. These can.

// wantPanic runs fn and returns what it panicked with, failing the test if it
// returned normally instead. Returning normally is the defect under test here:
// a silent wrong answer is exactly what the guards replaced.
func wantPanic(t *testing.T, what string, fn func()) string {
	t.Helper()
	var msg string
	func() {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			msg, _ = r.(string)
			if msg == "" {
				t.Fatalf("%s panicked with a %T, want a string naming the kind", what, r)
			}
		}()
		fn()
	}()
	if msg == "" {
		t.Fatalf("%s returned normally for a kind no feed can run -- it is deciding "+
			"something plausible instead of refusing, which is the silent defect these guards replaced", what)
	}
	return msg
}

// TestTheThreeFeedBuildersRefuseAKindTheyCannotBuild covers all three sites at
// once, because the danger was never one of them -- it was that a later task
// would teach two and miss the third, and the two that were taught would make
// the third's silence look like success.
func TestTheThreeFeedBuildersRefuseAKindTheyCannotBuild(t *testing.T) {
	e := failoverEngine(t)
	var buf bytes.Buffer
	e.log = slog.New(slog.NewTextHandler(&buf, nil))

	s := failoverOnSettings()
	setSettings(e, s)
	e.reconcileSelector(s, wantSelector(s), "")
	hub := e.selectorHub()
	if hub == nil {
		t.Fatal("the selector tier did not start")
	}
	t.Cleanup(func() {
		e.selMu.Lock()
		defer e.selMu.Unlock()
		e.teardownFeed(e.sel.feed)
		_ = hub.Close()
	})

	// Site 1: feedUpstreamSig. It returns a hash, and no hash means "refuse",
	// so it panics. Its message has to name every function that needs a case,
	// because the whole failure mode is teaching some of them and not the rest.
	msg := wantPanic(t, "feedUpstreamSig(sourcePlayout)", func() {
		e.feedUpstreamSig(s, sourcePlayout, "")
	})
	t.Logf("feedUpstreamSig panicked: %s", msg)
	for _, name := range []string{"feedUpstreamSig", "startFeed", "downstreamFeedInput", "playout"} {
		if !strings.Contains(msg, name) {
			t.Errorf("the panic does not mention %q: %s\n"+
				"the message is the only thing that tells the next person which sites still need a case", name, msg)
		}
	}

	// Site 2: downstreamFeedInput. It used to be `if kind == sourceBackup`
	// with everything else falling through to the primary's hub -- the
	// mechanism that actually made a mislabelled feed primary-shaped.
	msg = wantPanic(t, "downstreamFeedInput(sourcePlayout)", func() {
		e.downstreamFeedInput(sourcePlayout)
	})
	t.Logf("downstreamFeedInput panicked: %s", msg)
	if !strings.Contains(msg, "playout") {
		t.Errorf("the panic does not name the kind it refused: %s", msg)
	}

	// Site 3: startFeed. This one CAN report a failure -- fail() records it on
	// the tier and logs it -- so it returns an error rather than panicking, and
	// the important half of the assertion is that no process was started.
	buf.Reset()
	feed := e.startFeed(s, sourcePlayout, "sig", "", time.Now())
	if feed != nil {
		t.Errorf("startFeed built a feed for a kind it cannot run: kind=%s in=%v",
			feed.kind, feed.in != nil)
	}
	if logged := buf.String(); !strings.Contains(logged, "start source feed") || !strings.Contains(logged, "playout") {
		t.Errorf("startFeed did not report the refusal: %s", logged)
	} else {
		t.Logf("startFeed logged: %s", strings.TrimSpace(logged))
	}

	e.mu.RLock()
	recorded := e.sel.err
	e.mu.RUnlock()
	if !strings.Contains(recorded, "playout") {
		t.Errorf("the tier recorded %q, want the refusal an operator can read back through the API", recorded)
	}
}

// TestEnsureFeedHoldsTheRunningFeedRatherThanBuildingAKindItCannotBuild is the
// boundary that keeps the two panics above off every production path.
//
// It is the same division of labour chooseFrom and decideSource already use: the
// builder panics, and exactly one caller upstream of it turns that into "hold
// what you have and say why". Without this the loud failure would be a server
// crash on the sweep goroutine, which is not an improvement on a silent one.
func TestEnsureFeedHoldsTheRunningFeedRatherThanBuildingAKindItCannotBuild(t *testing.T) {
	e := failoverEngine(t)
	var buf bytes.Buffer
	e.log = slog.New(slog.NewTextHandler(&buf, nil))

	s := failoverOnSettings()
	setSettings(e, s)
	e.reconcileSelector(s, wantSelector(s), "")
	hub := e.selectorHub()
	if hub == nil {
		t.Fatal("the selector tier did not start")
	}
	t.Cleanup(func() {
		e.selMu.Lock()
		defer e.selMu.Unlock()
		e.teardownFeed(e.sel.feed)
		_ = hub.Close()
	})

	e.mu.RLock()
	feedBefore, activeBefore, reasonBefore := e.sel.feed, e.sel.active, e.sel.reason
	e.mu.RUnlock()
	if feedBefore == nil {
		t.Fatal("no feed is running, so there is nothing to prove was held")
	}

	buf.Reset()
	e.selMu.Lock()
	e.ensureFeed(s, "", sourcePlayout, "the playlist is running", time.Now())
	e.selMu.Unlock()

	e.mu.RLock()
	feedAfter, activeAfter, reasonAfter, recorded := e.sel.feed, e.sel.active, e.sel.reason, e.sel.err
	e.mu.RUnlock()

	// The running feed is untouched. Not torn down and restarted, not replaced
	// by a primary-shaped one wearing the wrong label: the same pointer.
	if feedAfter != feedBefore {
		t.Errorf("the running feed was disturbed: was %p now %p -- a decision the feed layer "+
			"cannot carry out must not stop the one that is working", feedBefore, feedAfter)
	}
	if activeAfter != activeBefore {
		t.Errorf("sel.active moved to %s; want it left at %s. Recording a source that was never "+
			"started is the exact disagreement between bookkeeping and process this guards against",
			orNone(activeAfter), orNone(activeBefore))
	}
	if reasonAfter != reasonBefore {
		t.Errorf("sel.reason moved to %q; an operator must not be told a switch happened when none did", reasonAfter)
	}
	if !strings.Contains(recorded, "playout") {
		t.Errorf("the tier recorded %q, want the reason the switch did not happen", recorded)
	}
	first := buf.String()
	if !strings.Contains(first, "no feed can run") {
		t.Errorf("the refusal was not logged: %s", first)
	}
	t.Logf("ensureFeed logged: %s", strings.TrimSpace(first))

	// The sweep runs twice a second and the cause cannot clear on its own, so
	// the second identical sweep must record the same fault and say nothing new.
	// A log storm is how a real fault becomes unreadable.
	buf.Reset()
	e.selMu.Lock()
	e.ensureFeed(s, "", sourcePlayout, "the playlist is running", time.Now())
	e.selMu.Unlock()
	if repeated := buf.String(); repeated != "" {
		t.Errorf("the same refusal logged again on the next sweep: %s", repeated)
	}
}
