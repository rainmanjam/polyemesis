package chat

import (
	"fmt"
	"testing"
)

// SetHistory resizes a RING, which is the part that is easy to get quietly
// wrong. A ring that has wrapped stores its entries out of order in the backing
// array, so any resize that reslices or copies naively produces scrollback that
// is subtly scrambled or silently truncated -- and nothing errors, because
// every index involved stays in range.
//
// So these tests care about two things above all: the ORDER that comes back,
// and that it is the NEWEST messages which survive a shrink. A browser
// connecting after a resize wants the most recent traffic, in the order it was
// said.

// fill puts n messages through the hub's history, numbered so order is
// checkable. It goes through the same path a real message takes.
func fill(h *Hub, n int) {
	for i := 0; i < n; i++ {
		h.mu.Lock()
		h.pushHistory(Message{ID: fmt.Sprintf("m%d", i)})
		h.mu.Unlock()
	}
}

func ids(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}
	return out
}

func TestSetHistoryKeepsTheNewestWhenItShrinks(t *testing.T) {
	h := New(WithHistory(10))
	defer h.Close()
	fill(h, 10)

	h.SetHistory(3)

	got := ids(h.History(0))
	want := []string{"m7", "m8", "m9"}
	if len(got) != len(want) {
		t.Fatalf("history has %d messages, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("history = %v, want %v -- a shrink must drop the OLDEST surplus", got, want)
		}
	}
}

// The case a naive resize gets wrong. A ring that has wrapped has its oldest
// entry in the middle of the backing array, so copying the array as it lies
// returns the scrollback rotated.
func TestSetHistoryPreservesOrderAcrossAWrappedRing(t *testing.T) {
	h := New(WithHistory(4))
	defer h.Close()
	// 6 into a ring of 4: it wraps, and m4/m5 sit at indices 0 and 1 while the
	// oldest surviving message m2 sits at index 2.
	fill(h, 6)

	before := ids(h.History(0))
	if len(before) != 4 || before[0] != "m2" || before[3] != "m5" {
		t.Fatalf("precondition failed: wrapped ring reads %v, want m2..m5", before)
	}

	h.SetHistory(8)

	got := ids(h.History(0))
	want := []string{"m2", "m3", "m4", "m5"}
	if len(got) != len(want) {
		t.Fatalf("after growing, history has %d messages, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("after growing a WRAPPED ring, history = %v, want %v", got, want)
		}
	}
}

// Growing must leave room that is actually usable, not just a bigger array with
// stale bookkeeping.
func TestSetHistoryGrowsAndThenAcceptsMore(t *testing.T) {
	h := New(WithHistory(3))
	defer h.Close()
	fill(h, 3)

	h.SetHistory(6)
	for i := 3; i < 6; i++ {
		h.mu.Lock()
		h.pushHistory(Message{ID: fmt.Sprintf("m%d", i)})
		h.mu.Unlock()
	}

	got := ids(h.History(0))
	if len(got) != 6 {
		t.Fatalf("history has %d messages after growing to 6 and filling it, want 6: %v", len(got), got)
	}
	if got[0] != "m0" || got[5] != "m5" {
		t.Errorf("history = %v, want m0..m5 in order", got)
	}
}

// Shrinking to exactly full must wrap the write cursor to 0 rather than leave it
// pointing one past the end -- the next push would panic or overwrite the wrong
// slot.
func TestSetHistoryLeavesAFullRingWritable(t *testing.T) {
	h := New(WithHistory(10))
	defer h.Close()
	fill(h, 10)

	h.SetHistory(4) // now exactly full: histN == len(history)

	h.mu.Lock()
	h.pushHistory(Message{ID: "next"})
	h.mu.Unlock()

	got := ids(h.History(0))
	want := []string{"m7", "m8", "m9", "next"}
	if len(got) != len(want) {
		t.Fatalf("history = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("history = %v, want %v -- the write cursor did not wrap", got, want)
		}
	}
}

func TestSetHistoryIgnoresNonsenseAndNoOps(t *testing.T) {
	h := New(WithHistory(5))
	defer h.Close()
	fill(h, 5)

	for _, n := range []int{0, -1} {
		h.SetHistory(n)
		if got := len(h.History(0)); got != 5 {
			t.Errorf("SetHistory(%d) changed the ring: %d messages left", n, got)
		}
	}

	// Same size is a no-op and must not discard anything.
	h.SetHistory(5)
	if got := ids(h.History(0)); len(got) != 5 || got[0] != "m0" {
		t.Errorf("SetHistory(same size) disturbed the ring: %v", got)
	}
}
