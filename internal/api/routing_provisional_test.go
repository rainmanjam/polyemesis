package api

import (
	"os"
	"strings"
	"testing"
)

func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// The routing preview must say when it was compiled from the placeholder.
//
// Every destination path returns a compiled filterComplex so the editor can
// show "Tracks 1, 2, 4 -> stereo" without a second round trip. Until a stream
// has been measured that graph is built from routing.DefaultSource() -- six
// stereo tracks that exist so the editor has something to draw -- and
// reconcileOutputs refuses to run it. Unlabelled, the operator was shown
// `[0:a:5]` and 2-channel pans presented as their destination's routing while
// the engine declined to start that exact graph.
//
// A source-level check because the alternative is standing up an engine with an
// unprobed layout through the HTTP stack, and the thing worth pinning is that
// no preview path silently drops the known bit again.
func TestEveryRoutingPreviewCarriesTheProvisionalFlag(t *testing.T) {
	src := readSource(t, "handlers.go")

	// Source() discards whether the layout is measured. Any preview compiled
	// from it is the bug this guards.
	if n := strings.Count(src, "routing.Compile(row.Profile, s.eng().Source())"); n != 0 {
		t.Errorf("%d destination preview(s) still compile against s.eng().Source(), "+
			"which drops the known bit; use SourceKnown so the response can say "+
			"the graph is provisional", n)
	}
	if strings.Count(src, "routingProvisional") < 3 {
		t.Errorf("only %d preview path(s) flag a placeholder-compiled graph, want at "+
			"least 3 (list, get, update). A preview the engine will not run has to "+
			"admit what it was compiled from",
			strings.Count(src, "routingProvisional"))
	}
	// Flagged, never refused: configuring a destination before going live is
	// when most people configure them, and refuseIfSilent argues that case
	// explicitly.
	if !strings.Contains(src, "func (s *Server) refuseIfSilent") {
		t.Error("refuseIfSilent is gone; the flag must not have become a refusal")
	}
}
