package api

// The levels feed state, asserted as a function of the last frame's age.
//
// A dial that keeps drawing its last bar after the meters have stopped is the
// lie this exists to prevent. The three states name what the caller can
// honestly show: "live" (a frame arrived inside the window), "stale" (frames
// used to arrive and have stopped), and "none" (no frame has ever arrived in
// this run of the engine).

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/events"
)

func TestLevelsFeedStateNamesTheHonestState(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		at       time.Time
		interval time.Duration
		want     string
	}{
		{name: "never received a frame", at: time.Time{}, interval: 100 * time.Millisecond, want: "none"},
		{name: "fresh frame", at: now.Add(-100 * time.Millisecond), interval: 100 * time.Millisecond, want: "live"},
		{name: "frames stopped", at: now.Add(-10 * time.Second), interval: 100 * time.Millisecond, want: "stale"},
		// Three intervals is the window, but never less than one second: at a
		// 40 ms interval a frame 800 ms old is still inside it.
		{name: "fast interval keeps the one second floor", at: now.Add(-800 * time.Millisecond), interval: 40 * time.Millisecond, want: "live"},
		// The boundary is inclusive: exactly the window is still live.
		{name: "exactly three intervals is live", at: now.Add(-1500 * time.Millisecond), interval: 500 * time.Millisecond, want: "live"},
		{name: "one tick past three intervals is stale", at: now.Add(-1501 * time.Millisecond), interval: 500 * time.Millisecond, want: "stale"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := levelsFeedState(tc.at, now, tc.interval); got != tc.want {
				t.Errorf("levelsFeedState(at=%v, interval=%v) = %q, want %q", tc.at, tc.interval, got, tc.want)
			}
		})
	}
}

// A read-scoped socket is how a dial gets its meters without polling. The
// levels frame must reach it with its readings intact. Levels carry no stored
// field, so the policy is passthrough, and this pins that: a later change that
// drops levels for read tokens would silently blank every dial.
func TestLevelsFramesReachAReadScopedSocketIntact(t *testing.T) {
	payload := map[string]any{"channels": []any{-12.5, -14.0}}
	ev := events.Event{Type: events.TypeLevels, Data: payload}

	out, send := eventView(ev, true)
	if !send {
		t.Fatalf("a levels frame was withheld from a read-scoped socket")
	}
	if out.Type != events.TypeLevels {
		t.Errorf("type = %q, want %q", out.Type, events.TypeLevels)
	}
	if got, ok := out.Data.(map[string]any); !ok || len(got) != len(payload) {
		t.Errorf("payload changed on its way to a read socket: %#v", out.Data)
	}
}
