package main

import (
	"context"
	"log/slog"
	"testing"
)

// TestNilHooksAreInert pins the contract the interactive path depends on:
// run(nil) must never branch on hooks being present.
func TestNilHooksAreInert(t *testing.T) {
	var h *hooks

	tests := []struct {
		name string
		call func()
	}{
		{"progress on nil hooks does nothing", func() { h.progress("detecting ffmpeg") }},
		{"ready on nil hooks does nothing", func() { h.ready() }},
		{"stopping on nil hooks does nothing", func() { h.stopping() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.call() // a panic here fails the test
		})
	}

	t.Run("stopped on nil hooks yields a channel that never fires", func(t *testing.T) {
		ch := h.stopped()
		if ch != nil {
			t.Fatalf("stopped() = %v, want nil so select blocks on it forever", ch)
		}
		select {
		case <-ch:
			t.Fatal("a nil stop channel became ready")
		default:
		}
	})

	t.Run("logger on nil hooks falls back to stderr", func(t *testing.T) {
		if h.logger("info") == nil {
			t.Fatal("logger() = nil, want the stderr logger")
		}
	})
}

// TestHooksCallbacksAreInvoked pins that a service manager actually observes
// every lifecycle transition it registered for.
func TestHooksCallbacksAreInvoked(t *testing.T) {
	var seen []string
	stop := make(chan struct{})
	h := &hooks{
		Progress: func(p string) { seen = append(seen, "progress:"+p) },
		Ready:    func() { seen = append(seen, "ready") },
		Stopping: func() { seen = append(seen, "stopping") },
		Stop:     stop,
	}

	h.progress("opening the database")
	h.ready()
	h.stopping()

	want := []string{"progress:opening the database", "ready", "stopping"}
	if len(seen) != len(want) {
		t.Fatalf("callbacks fired = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("callback %d = %q, want %q", i, seen[i], want[i])
		}
	}

	if got := h.stopped(); got == nil {
		t.Fatal("stopped() = nil, want the configured stop channel")
	}
	close(stop)
	select {
	case <-h.stopped():
	default:
		t.Fatal("stopped() did not observe the closed stop channel")
	}
}

// TestHooksLoggerHonoursTheLogFlag pins that swapping in a service log sink
// does not silently discard the -log level the operator asked for.
func TestHooksLoggerHonoursTheLogFlag(t *testing.T) {
	tests := []struct {
		name  string
		flag  string
		want  slog.Level
		emits bool // whether an info record survives the level
	}{
		{"debug flag admits info records", "debug", slog.LevelDebug, true},
		{"info flag admits info records", "info", slog.LevelInfo, true},
		{"warn flag drops info records", "warn", slog.LevelWarn, false},
		{"error flag drops info records", "error", slog.LevelError, false},
		{"unrecognised flag defaults to info", "chatty", slog.LevelInfo, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotLevel slog.Level
			cap := &captureHandler{}
			h := &hooks{NewHandler: func(l slog.Level) slog.Handler {
				gotLevel = l
				cap.level = l
				return cap
			}}

			log := h.logger(tt.flag)
			if gotLevel != tt.want {
				t.Errorf("handler built at level %v, want %v", gotLevel, tt.want)
			}
			log.Info("ffmpeg detected")
			if got := len(cap.records) > 0; got != tt.emits {
				t.Errorf("info record delivered = %v, want %v", got, tt.emits)
			}
		})
	}
}

func TestParseLevelMapsFlagStringsToSlogLevels(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want slog.Level
	}{
		{"debug", "debug", slog.LevelDebug},
		{"info", "info", slog.LevelInfo},
		{"warn", "warn", slog.LevelWarn},
		{"error", "error", slog.LevelError},
		{"mixed case is accepted", "WARN", slog.LevelWarn},
		{"empty defaults to info", "", slog.LevelInfo},
		{"unknown defaults to info", "verbose", slog.LevelInfo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseLevel(tt.in); got != tt.want {
				t.Errorf("parseLevel(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

type captureHandler struct {
	level   slog.Level
	records []slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }
