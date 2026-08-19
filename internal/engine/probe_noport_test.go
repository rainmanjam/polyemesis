package engine

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/relay"
)

/* THE HOLD'S EXIT, EXERCISED RATHER THAN READ.
 *
 * probe_giveup_test.go pins the SHAPE of these branches by reading engine.go as
 * text, which is the right guard for an ordering property -- a reset that moves
 * above a return is invisible to any test that only runs the code. But a source
 * assertion executes nothing, so the branch it protects can be entirely dead and
 * still pass.
 *
 * This runs it. A destination is held until the ingest layout is measured, and
 * the wait ends only after probeGiveUp CONSECUTIVE failures -- so a probe that
 * cannot even get a relay port has to count, or a box too loaded to probe is a
 * box whose destinations never come up and whose log never says why.
 */
func TestAProbeWithNoFreePortCountsAndSaysSo(t *testing.T) {
	var logged bytes.Buffer
	e := &Engine{
		log:      slog.New(slog.NewTextHandler(&logged, nil)),
		sourceID: 1,
		// Span zero: Allocate's loop body never runs, so every call fails. That
		// is the shape of an exhausted range without needing to exhaust one.
		alloc: relay.NewPortAllocator(20000, 0),
	}

	if e.probeOnce(context.Background()) {
		t.Fatal("probeOnce reported a layout change without a port to probe through")
	}
	if got := e.probeFails.Load(); got != 1 {
		t.Errorf("probeFails = %d after a probe that could not start, want 1. "+
			"A probe that never ran measured nothing, and the hold's exit counts "+
			"non-measurements -- not just the ones ffprobe got far enough to fail", got)
	}

	// THE PROPERTY THAT MATTERS: the wait actually ends.
	//
	// BOUNDED, and the bound is not decoration. Written as
	// `for e.probeFails.Load() < probeGiveUp`, a regression that stops counting
	// turns this into an infinite loop -- the mutation is still caught, but as a
	// ten-minute package timeout naming no test instead of an assertion naming
	// the property. A test whose failure mode is a hang is a test that will be
	// disabled rather than read.
	for i := 0; i < probeGiveUp*2 && !e.probeUnmeasurable(); i++ {
		e.probeOnce(context.Background())
	}
	if !e.probeUnmeasurable() {
		t.Fatal("the layout is still not declared unmeasurable after repeated " +
			"failures to get a port, so every destination stays held indefinitely")
	}

	out := logged.String()
	if !strings.Contains(out, "no free relay port") {
		t.Errorf("nothing in the log names the missing port; an operator whose "+
			"destinations are all down has no way to find out why. got: %q", out)
	}
	if !strings.Contains(out, "cannot be measured") {
		t.Error("the transition out of the hold is not announced, so destinations " +
			"that come up on an approximate mix do so with no explanation")
	}
}
