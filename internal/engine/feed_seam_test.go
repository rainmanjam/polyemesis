package engine

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A feed's timeline must not begin BEHIND where the outgoing feed's ended.
//
// -output_ts_offset pins a feed's timeline to the offset it is given, not to
// when its frames actually appear, so the two feeds' timestamps meet exactly
// where the arithmetic says they do. If the incoming feed's offset is derived
// from a moment BEFORE the outgoing one was stopped, the seam steps backwards
// by however long stopping took -- and a platform drops the connection on a
// backwards DTS, which is the failover tier failing at its one job.
//
// This is the arithmetic, isolated. It is not proof that FFmpeg emits a
// backwards DTS -- see issue #126 -- but it is the mechanism that would produce
// one, and it is exact.
func TestAFeedTimelineDoesNotBeginBehindTheOneItReplaces(t *testing.T) {
	tierStart := time.Now()

	// The outgoing feed has been running a while.
	outgoingStart := tierStart.Add(30 * time.Second)
	// The swap is decided here.
	decidedAt := tierStart.Add(90 * time.Second)
	// Stopping the old process BLOCKS. teardownFeed waits on proc.Stop with
	// stopTimeout, which is 12s; a slow exit spends real time here.
	const teardown = 800 * time.Millisecond
	actuallyStartedAt := decidedAt.Add(teardown)

	// Where the outgoing feed's timestamps had reached when it finally stopped.
	outgoingLast := outgoingStart.Sub(tierStart).Seconds() +
		actuallyStartedAt.Sub(outgoingStart).Seconds()

	// What the incoming feed is given, from a time captured BEFORE the teardown.
	beforeTeardown := feedOffset(tierStart, decidedAt)
	if gap := outgoingLast - beforeTeardown; gap < 0.001 {
		t.Fatalf("precondition: no gap to measure (%.3fs)", gap)
	} else {
		t.Logf("offset from the pre-teardown time is %.3fs BEHIND the outgoing "+
			"feed's last timestamp -- exactly the teardown duration", gap)
	}

	// What it must be given: a time captured after the teardown.
	afterTeardown := feedOffset(tierStart, actuallyStartedAt)
	if afterTeardown < outgoingLast-0.001 {
		t.Errorf("even after the teardown the timeline begins %.3fs behind; the seam "+
			"steps backwards and a platform drops the connection on that",
			outgoingLast-afterTeardown)
	}
}

// The fix has to be WIRED IN: the time handed to startFeed must be read AFTER
// teardownFeed returns, not before it is called.
func TestTheFeedOffsetIsTakenAfterTheTeardown(t *testing.T) {
	b, err := os.ReadFile("selector.go")
	if err != nil {
		t.Fatalf("read selector.go: %v", err)
	}
	src := string(b)

	at := strings.Index(src, "e.teardownFeed(cur)")
	if at < 0 {
		t.Fatal("cannot find the teardown in ensureFeed")
	}
	after := src[at:]
	end := strings.Index(after, "e.mu.Lock()")
	if end > 0 {
		after = after[:end]
	}
	if !strings.Contains(after, "startedAt := time.Now()") {
		t.Error("the time handed to startFeed is still captured before teardownFeed, " +
			"which blocks on proc.Stop for up to stopTimeout. The incoming feed's " +
			"timeline then begins behind the outgoing one's by however long stopping " +
			"took, and that is a backwards DTS at every switch")
	}
}

// The respawn backoff measures from when the feed was STARTED, not from when
// the switch was decided.
//
// feedAt used to take the pre-teardown time, with a comment claiming that was
// "the right thing for a backoff". It is the opposite. teardownFeed blocks for
// as long as the outgoing process takes to exit -- up to stopTimeout, which is
// 12 seconds -- so recording a moment before it means feedRespawn has already
// elapsed by the time the replacement is started. A feed that then fails to
// start is retried on the very next 500ms sweep, and every sweep after it:
// exactly the spawn-twice-a-second loop the backoff exists to prevent.
func TestTheRespawnBackoffMeasuresFromTheStartNotTheDecision(t *testing.T) {
	src := readEngineFile(t, "selector.go")
	body := funcBody(t, src, "func (e *Engine) ensureFeed(")

	if !strings.Contains(body, "e.sel.feedAt = startedAt") {
		t.Error("feedAt is not the post-teardown time. After a slow teardown the " +
			"backoff window has already expired when the feed starts, so a failing " +
			"feed respawns on every sweep")
	}
	if strings.Contains(body, "e.sel.feedAt = now\n") {
		t.Error("feedAt still takes the pre-teardown decision time")
	}
	// switchedAt is deliberately the decision time: it is shown to an operator
	// as when the switch happened, and that is when it was decided rather than
	// when the outgoing process finally exited.
	if !strings.Contains(body, "e.sel.switchedAt = now") {
		t.Error("switchedAt no longer records the decision time, which is what an " +
			"operator is shown as the moment of the switch")
	}
}
