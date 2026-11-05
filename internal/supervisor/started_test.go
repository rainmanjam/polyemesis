package supervisor

import (
	"io"
	"log/slog"
	"testing"
)

// Started tells a process that was built from one that was started: a delayed
// destination's process exists while its buffer fills and is started later, and
// a caller asking "is this destination running" must not read the first as the
// second (engine.DestinationRunOf).
//
// MUTATION: Started returns p.running alone, or true. Observed: FAIL on the
// never-started or the stopped read.
func TestStartedIsTrueOnlyBetweenStartAndStop(t *testing.T) {
	p := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Spec{
		Name: "started-probe", Kind: "destination", Bin: "/nonexistent/ffmpeg",
	})
	if p.Started() {
		t.Fatal("a process that was never started reads as started")
	}
	p.Start()
	if !p.Started() {
		t.Fatal("a started process does not read as started")
	}
	if err := p.Stop(t.Context()); err != nil {
		t.Logf("stop: %v", err)
	}
	if p.Started() {
		t.Fatal("a stopped process still reads as started")
	}
}
