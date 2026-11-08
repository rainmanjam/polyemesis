package overlay

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
)

func expectNothing(t *testing.T, sub *eventstream.Subscription, why string) {
	t.Helper()
	if env, ok := next(t, sub); ok {
		t.Fatalf("%s: got %+v", why, env)
	}
}

func TestAHeldMessageIsNotShownUntilReleased(t *testing.T) {
	g, _, sub, c := newGate(t)
	g.Offer(m("m1", "maybe a slur"), chat.DisplayHeld)
	expectNothing(t, sub, "a held message was shown")
	if st := g.Stats(); st.Blocked != 1 || st.Held != 1 || st.Shown != 0 {
		t.Fatalf("stats after hold = %+v", st)
	}

	c.add(2 * time.Minute)
	if !g.Release(db.PlatformTwitch, "acct", "m1") {
		t.Fatal("Release found nothing to release")
	}
	env, ok := next(t, sub)
	if !ok || env.Type != eventstream.TypeChatDisplay {
		t.Fatalf("released line: %+v", env)
	}
	line := env.Data.(eventstream.DisplayLine)
	if line.Text != "maybe a slur" {
		t.Errorf("line = %+v", line)
	}
	// The envelope is stamped at release (the replay window reads it); the
	// line keeps the time it was said.
	if !env.At.Equal(c.now()) || !line.At.Equal(m("m1", "").At) {
		t.Errorf("envelope At %v (want %v), line At %v (want %v)", env.At, c.now(), line.At, m("m1", "").At)
	}
	if st := g.Stats(); st.Released != 1 || st.Shown != 1 {
		t.Fatalf("stats after release = %+v", st)
	}
	// Once: a second click finds nothing and shows nothing.
	if g.Release(db.PlatformTwitch, "acct", "m1") {
		t.Error("released twice")
	}
	expectNothing(t, sub, "a second release published")
}

// A line that was REMOVED rather than held has no way back.
func TestABlockedMessageCannotBeReleased(t *testing.T) {
	g, _, sub, _ := newGate(t)
	g.Offer(m("m1", "deleted"), chat.DisplayBlocked)
	if g.Release(db.PlatformTwitch, "acct", "m1") {
		t.Fatal("released a removed message")
	}
	if g.Release(db.PlatformTwitch, "acct", "never-seen") {
		t.Fatal("released a message the gate never saw")
	}
	expectNothing(t, sub, "release of a non-held line published")
}

func TestAHeldLineExpiresAfterTenMinutes(t *testing.T) {
	g, _, sub, c := newGate(t)
	g.Offer(m("m1", "old"), chat.DisplayHeld)
	c.add(heldTTL + time.Second)
	if g.Release(db.PlatformTwitch, "acct", "m1") {
		t.Fatal("released a line past its ttl")
	}
	expectNothing(t, sub, "an expired line was published")

	// Sweep frees the memory without anyone clicking.
	g.Offer(m("m2", "old too"), chat.DisplayHeld)
	c.add(heldTTL + time.Second)
	g.Sweep()
	g.mu.Lock()
	n := len(g.heldLines) + len(g.heldOrder)
	g.mu.Unlock()
	if n != 0 {
		t.Errorf("sweep left %d held entries", n)
	}
}

func TestTheHeldBufferKeepsOnlyTheLastTwoHundred(t *testing.T) {
	g, _, _, _ := newGate(t)
	for i := 0; i < heldCap+5; i++ {
		g.Offer(m(string(rune('a'+i%26))+string(rune('A'+i/26)), "x"), chat.DisplayHeld)
	}
	g.mu.Lock()
	n, o := len(g.heldLines), len(g.heldOrder)
	g.mu.Unlock()
	if n != heldCap || o != heldCap {
		t.Fatalf("held %d lines / %d order entries, want %d", n, o, heldCap)
	}
	if g.Release(db.PlatformTwitch, "acct", "aA") {
		t.Error("the oldest line survived the cap")
	}
}

// Offering the same line twice keeps one entry.
func TestAHeldLineOfferedTwiceIsKeptOnce(t *testing.T) {
	g, _, sub, _ := newGate(t)
	g.Offer(m("m1", "again"), chat.DisplayHeld)
	g.Offer(m("m1", "again"), chat.DisplayHeld)
	g.mu.Lock()
	n := len(g.heldOrder)
	g.mu.Unlock()
	if n != 1 {
		t.Fatalf("order entries = %d", n)
	}
	g.Release(db.PlatformTwitch, "acct", "m1")
	next(t, sub)
	expectNothing(t, sub, "a duplicate hold published twice")
}

// The operator's removal outranks the earlier "maybe".
func TestADeletedHeldLineCannotBeReleased(t *testing.T) {
	g, _, sub, _ := newGate(t)
	g.Offer(m("m1", "held then deleted"), chat.DisplayHeld)
	g.Retract(db.PlatformTwitch, "acct", []string{"m1"})
	if g.Release(db.PlatformTwitch, "acct", "m1") {
		t.Fatal("released a line the operator had deleted")
	}
	expectNothing(t, sub, "published")
}

// A model verdict of Held on a pending line withholds it and keeps it.
func TestAPendingLineJudgedHeldIsKeptForRelease(t *testing.T) {
	g, _, sub, _ := newGate(t)
	g.Offer(m("m1", "pending"), chat.DisplayPending)
	g.Judge(db.PlatformTwitch, "acct", "m1", chat.DisplayHeld)
	expectNothing(t, sub, "a held judgement showed the line")
	if !g.Release(db.PlatformTwitch, "acct", "m1") {
		t.Fatal("a line judged Held was not releasable")
	}
	if _, ok := next(t, sub); !ok {
		t.Fatal("nothing published on release")
	}
}

// A line the fast pass held that the model then removes is no longer a maybe.
func TestAHeldLineTheModelThenRemovesCannotBeReleased(t *testing.T) {
	g, _, sub, _ := newGate(t)
	g.Offer(m("m1", "held"), chat.DisplayHeld)
	g.Judge(db.PlatformTwitch, "acct", "m1", chat.DisplayBlocked)
	if g.Release(db.PlatformTwitch, "acct", "m1") {
		t.Fatal("released a line the model removed")
	}
	expectNothing(t, sub, "published")
}

// A released line can then be retracted like any other.
func TestAReleasedLineCanStillBeRetracted(t *testing.T) {
	g, _, sub, _ := newGate(t)
	g.Offer(m("m1", "x"), chat.DisplayHeld)
	g.Release(db.PlatformTwitch, "acct", "m1")
	next(t, sub)
	g.Retract(db.PlatformTwitch, "acct", []string{"m1"})
	env, ok := next(t, sub)
	if !ok || env.Type != eventstream.TypeChatUndisplay {
		t.Fatalf("got %+v, want an undisplay", env)
	}
}
