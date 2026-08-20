package engine

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func previewSettings(idle int) db.Settings {
	s := db.DefaultSettings()
	s.Preview.IdleTimeoutSeconds = idle
	return s
}

func TestPreviewIdleWindow(t *testing.T) {
	tests := []struct {
		name string
		idle int
		want time.Duration
	}{
		{"unset idle timeout falls back to the built-in default", 0, previewIdleDefault},
		{"negative idle timeout falls back to the built-in default", -5, previewIdleDefault},
		{"configured idle timeout is honoured", 90, 90 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := previewIdleWindow(previewSettings(tt.idle)); got != tt.want {
				t.Errorf("previewIdleWindow() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPreviewIdle(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		idle  int
		since time.Duration
		want  bool
	}{
		{"a page reload does not idle the encoder out", 30, 3 * time.Second, false},
		{"polling within the window keeps the encoder alive", 30, 29 * time.Second, false},
		{"the window boundary stops the encoder", 30, 30 * time.Second, true},
		{"a closed dashboard stops the encoder", 30, 5 * time.Minute, true},
		{"a longer configured window defers the stop", 300, 2 * time.Minute, false},
		{"never having been requested counts as idle", 30, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A zero seen time is what a never-requested preview carries, and
			// must read as idle rather than as "requested at the epoch".
			seen := time.Time{}
			if tt.since != 0 {
				seen = now.Add(-tt.since)
			}
			if got := previewIdle(previewSettings(tt.idle), seen, now); got != tt.want {
				t.Errorf("previewIdle(seen=-%v) = %v, want %v", tt.since, got, tt.want)
			}
		})
	}
}

func TestPreviewSigIgnoresIdleTimeout(t *testing.T) {
	base := previewSettings(30)
	longer := previewSettings(600)

	if previewSig(base) != previewSig(longer) {
		t.Error("changing the idle timeout changed the restart signature; it would cycle a live preview")
	}

	tests := []struct {
		name  string
		apply func(*db.Settings)
	}{
		{"segment length change restarts the encoder", func(s *db.Settings) { s.Preview.SegmentSeconds = 4 }},
		{"height change restarts the encoder", func(s *db.Settings) { s.Preview.VideoHeight = 720 }},
		{"bitrate change restarts the encoder", func(s *db.Settings) { s.Preview.VideoKbps = 1500 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := previewSettings(30)
			tt.apply(&changed)
			if previewSig(base) == previewSig(changed) {
				t.Error("signature unchanged; the encoder would keep stale arguments")
			}
		})
	}
}

// A NEW HUB IS NOT DELIVERY.
//
// previewFlowing decides whether anything is on air by watching a byte counter
// advance. A counter is only comparable with ITSELF: when the selector comes up
// or goes down the hub is replaced, and the replacement starts at zero. Compared
// against the old hub's total that reads as "the number changed" -- the exact
// opposite of the truth -- so the gate would report a live output and start an
// encoder against silence for the whole grace period.
//
// Found in review rather than by the suite, which is why it is pinned.
func TestASwappedHubDoesNotCountAsFlow(t *testing.T) {
	e := lifeEngine(t)
	// FATAL, NOT SKIP. Without a hub this test asserts nothing, and a test that
	// declines to run still prints ok -- which is the free pass the skip census
	// exists to refuse. The fixture does provide one; if that ever stops being
	// true the fixture is broken and should say so.
	if e.downstreamHub() == nil {
		t.Fatal("fixture: lifeEngine has no downstream hub, so there is nothing to " +
			"sample and the hub-identity property cannot be exercised")
	}

	// A baseline remembered from a DIFFERENT hub, carrying a large total, and
	// stamped as though it had just advanced. That is the state a selector swap
	// leaves behind.
	e.mu.Lock()
	e.previewRxHub = nil
	e.previewRxBytes = 1 << 20
	e.previewRxAt = time.Now()
	e.mu.Unlock()

	if e.previewFlowing(time.Now()) {
		t.Error("a hub whose counter has not been SEEN to advance read as flowing. " +
			"Its zero differs from the remembered total, and treating a difference " +
			"as delivery starts an encoder against silence after every selector swap")
	}

	// And it adopts, so the next real advance is measured against the right
	// baseline rather than being missed.
	e.mu.RLock()
	adopted := e.previewRxHub == e.downstreamHub()
	e.mu.RUnlock()
	if !adopted {
		t.Error("the new hub was not adopted as the baseline, so every later sample " +
			"compares against a hub that is gone")
	}
}
