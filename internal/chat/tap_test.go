package chat

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// tapRecorder writes down every Tap call, in order, one line each.
type tapRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *tapRecorder) Offer(m Message, d Display) {
	r.add(fmt.Sprintf("offer %s/%s %s %d", m.Platform, m.Account, m.ID, d))
}
func (r *tapRecorder) Judge(p db.Platform, account, id string, d Display) {
	r.add(fmt.Sprintf("judge %s/%s %s %d", p, account, id, d))
}
func (r *tapRecorder) RetractMessage(p db.Platform, account, id string) {
	r.add(fmt.Sprintf("message %s/%s %s", p, account, id))
}
func (r *tapRecorder) RetractAuthor(p db.Platform, account, author string) {
	r.add(fmt.Sprintf("author %s/%s %s", p, account, author))
}
func (r *tapRecorder) ClearRoom(p db.Platform, account string) {
	r.add(fmt.Sprintf("room %s/%s", p, account))
}
func (r *tapRecorder) add(l string) {
	r.mu.Lock()
	r.calls = append(r.calls, l)
	r.mu.Unlock()
}
func (r *tapRecorder) has(l string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if c == l {
			return true
		}
	}
	return false
}
func (r *tapRecorder) waitFor(t *testing.T, l string) {
	t.Helper()
	waitFor(t, l, func() bool { return r.has(l) })
}

// scriptedRun is a runFn that hands the sink each step in turn, then blocks.
func scriptedRun(steps ...func(Sink)) func(context.Context, Sink) error {
	return func(ctx context.Context, sink Sink) error {
		for _, s := range steps {
			s(sink)
		}
		<-ctx.Done()
		return nil
	}
}

// TestTheTapIsToldWhyAMessageWentNotOnlyThatItWent is the reason a Tap exists
// beside the DisplaySink. The overlay only needs "these ids are gone", and
// gets the same list for a deleted message, a banned viewer and a cleared
// room. The poll counter cannot work from that list: a cleared room is not a
// verdict on anybody's vote, a ban is a verdict on the PERSON whatever is still
// in the history ring, and a deleted message voids that one message. So the
// tap hears each of the three as itself.
func TestTheTapIsToldWhyAMessageWentNotOnlyThatItWent(t *testing.T) {
	h := testHub(t)
	tap := &tapRecorder{}
	h.SetTap(tap)
	a := &fakeAdapter{platform: db.PlatformTwitch, account: "acct", runFn: scriptedRun(
		func(s Sink) { s.Deliver(msg("m1", "u1", "1")) },
		func(s Sink) { retract(s, "m1") },
		func(s Sink) { retractUser(s, "u2") },
		func(s Sink) { retractUser(s, "") },
	)}
	attach(t, h, a)

	tap.waitFor(t, fmt.Sprintf("offer twitch/acct m1 %d", DisplayClear))
	tap.waitFor(t, "message twitch/acct m1")
	tap.waitFor(t, "author twitch/acct u2")
	tap.waitFor(t, "room twitch/acct")
	// A room clear is NOT also reported as an author retraction of "".
	if tap.has("author twitch/acct ") {
		t.Error("a room clear was reported as an author retraction with no author")
	}
}

// TestABanReachesTheTapEvenWhenNothingOfTheirsIsStillHeld: the history ring is
// bounded, and retractUser resolves message ids only from what it still holds.
// A viewer who voted early in a busy chat has scrolled out of it by the time a
// moderator bans them. The overlay has nothing to take down then; the poll
// still has a vote to void. The author signal must not depend on the ring.
func TestABanReachesTheTapEvenWhenNothingOfTheirsIsStillHeld(t *testing.T) {
	h := testHub(t, WithHistory(1))
	tap := &tapRecorder{}
	h.SetTap(tap)
	a := &fakeAdapter{platform: db.PlatformTwitch, account: "acct", runFn: scriptedRun(
		func(s Sink) { s.Deliver(msg("m1", "voter", "2")) },
		// Evicts the voter's message from a one-line ring.
		func(s Sink) { s.Deliver(msg("m2", "other", "hello")) },
		func(s Sink) { retractUser(s, "voter") },
	)}
	attach(t, h, a)
	tap.waitFor(t, "author twitch/acct voter")
}

// TestAnOperatorBanReachesTheTapAsAnAuthor: the hub's own Ban, not only a
// platform's CLEARCHAT, says which person was removed.
func TestAnOperatorBanReachesTheTapAsAnAuthor(t *testing.T) {
	h := testHub(t)
	tap := &tapRecorder{}
	h.SetTap(tap)
	b := &banningAdapter{fakeAdapter: &fakeAdapter{platform: db.PlatformYouTube, account: "yt"}}
	if err := h.Attach(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "attached", func() bool { _, ok := h.Adapter(db.PlatformYouTube, "yt"); return ok })
	if err := h.Ban(context.Background(), db.PlatformYouTube, "yt", "troll", 0, "spam"); err != nil {
		t.Fatal(err)
	}
	tap.waitFor(t, "author youtube/yt troll")
}

// TestTheTapSeesTheSameVerdictsAsTheOverlay: Offer and Judge reach the tap
// with exactly what the display sink is told, so a vote and an overlay line
// can never be judged differently.
func TestTheTapSeesTheSameVerdictsAsTheOverlay(t *testing.T) {
	h := testHub(t)
	tap := &tapRecorder{}
	sink := &displayRecorder{}
	h.SetTap(tap)
	h.SetDisplay(sink)
	h.judgeDisplay(db.PlatformKick, "k", "m9", DisplayBlocked)
	tap.waitFor(t, fmt.Sprintf("judge kick/k m9 %d", DisplayBlocked))
	sink.waitFor(t, fmt.Sprintf("judge m9 %d", DisplayBlocked))

	// Detaching is nil, and a hub with no tap is the ordinary case.
	h.SetTap(nil)
	h.judgeDisplay(db.PlatformKick, "k", "m10", DisplayClear)
	if tap.has(fmt.Sprintf("judge kick/k m10 %d", DisplayClear)) {
		t.Error("a detached tap was still called")
	}
}
