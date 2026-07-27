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
