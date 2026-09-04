package supervisor

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

// The only property that must never break: forgetting is safe.
//
// A shorter grace on a process that flushes is a truncated file that is exactly
// the right size on disk -- undetectable by anything short of playing it. So the
// tests that matter here are not "meters is fast", they are "everything else is
// not", and "a kind nobody has classified is not".

func TestAnUnknownKindGetsTheFullGrace(t *testing.T) {
	// The load-bearing case. Someone adds a process in six months, does not
	// read grace.go, and their teardown is merely slower than it could be --
	// never lossier than it should be.
	for _, kind := range []string{"", "some-future-kind", "recorder-v2", "unknown"} {
		if got := graceFor(kind); got != shutdownGrace {
			t.Errorf("graceFor(%q) = %v, want the full %v: an unclassified kind must default to safe",
				kind, got, shutdownGrace)
		}
	}
}

func TestEveryKindThatWritesSomethingKeepsTheFullGrace(t *testing.T) {
	// Each of these has an output a reader may later look at: a recording, a
	// file destination, HLS segments on disk, or the feed everything else is
	// built from. None may be shortened without reading its argv again.
	for _, kind := range []string{"recorder", "destination", "preview", "ingest", "source", "rendition", "playout"} {
		if got := graceFor(kind); got != shutdownGrace {
			t.Errorf("graceFor(%q) = %v, want %v -- this kind writes something", kind, got, shutdownGrace)
		}
	}
}

func TestKindsWithNoOutputExitFast(t *testing.T) {
	// Verified against the argv each one builds: meters and loudness are
	// -f null, silence muxes mpegts to a udp:// relay. Nothing on disk.
	for _, kind := range []string{"meters", "loudness", "silence"} {
		got := graceFor(kind)
		if got != fastGrace {
			t.Errorf("graceFor(%q) = %v, want %v", kind, got, fastGrace)
		}
		if got >= shutdownGrace {
			t.Errorf("graceFor(%q) is not actually shorter than the default", kind)
		}
	}
}

func TestFastGraceIsGenerousForAHealthyExit(t *testing.T) {
	// internal/engine/manager.go records the measurement: SIGTERM with the
	// input still flowing exits in 0.105s. Anything at or below that is a
	// deadline a healthy process could miss, which would turn this
	// optimisation into a source of the very kills it exists to shorten.
	const measuredHealthyExit = 105 * time.Millisecond
	if fastGrace < 5*measuredHealthyExit {
		t.Errorf("fastGrace = %v is too close to the measured healthy exit of %v",
			fastGrace, measuredHealthyExit)
	}
}

func TestTheShortListIsShort(t *testing.T) {
	// Not a style rule. Every entry is a promise that killing that process
	// mid-write destroys nothing, and each one had to be checked by reading its
	// argv. A list that grows quietly is a list nobody re-checked -- so make
	// growth require touching this assertion and reading why.
	if len(shortGraceKinds) > 3 {
		t.Errorf("shortGraceKinds has %d entries; each must be justified by its argv in grace.go",
			len(shortGraceKinds))
	}
}

func TestASpecGetsTheGraceItsKindDeserves(t *testing.T) {
	// End to end through New, because graceFor being right is no use if the
	// constructor ignores it.
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	fast := New(quiet, Spec{Name: "meters", Kind: "meters", Bin: "/bin/true"})
	if fast.grace != fastGrace {
		t.Errorf("meters Process got grace %v, want %v", fast.grace, fastGrace)
	}
	slow := New(quiet, Spec{Name: "recorder", Kind: "recorder", Bin: "/bin/true"})
	if slow.grace != shutdownGrace {
		t.Errorf("recorder Process got grace %v, want %v", slow.grace, shutdownGrace)
	}
}
