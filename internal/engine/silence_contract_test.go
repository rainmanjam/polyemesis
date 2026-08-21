package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// wantSilence's contract, pinned.
//
// This is what #161 asked for when it quarantined the video-only case: "pin the
// expected tier for a video-only source and fail on drift". The test that was
// quarantined could not do it, because it asserted the tier through
// reconcileOutputs and so could only observe the answer for one arrangement of
// half a dozen fields. Pinned here directly instead, one property per case.
func TestWantSilenceRaisesATierOnlyForAMeasuredVideoOnlySource(t *testing.T) {
	// The signature is a constant by construction -- nothing configurable feeds
	// the tier's command line yet -- so drift in it is drift in the tier itself,
	// and every destination downstream is compiled against the layout it names.
	want := hashStrings([]string{"silence", "stereo", "48000"})

	audio := routing.Source{Tracks: []routing.Track{{Channels: 2}}}

	for _, tc := range []struct {
		name     string
		on       bool
		measured bool
		src      routing.Source
		want     string
	}{
		{"measured video-only raises the tier", true, true, routing.Source{}, want},
		{"switched off raises nothing", false, true, routing.Source{}, ""},
		// THE PROPERTY THE QUARANTINED TEST GOT WRONG. An unmeasured layout
		// raises no tier, deliberately: a probe that can never succeed must not
		// be papered over with a synthesised bed, because then the hold's exit
		// could never be reached and the real fault would never surface.
		{"unmeasured raises nothing, even video-only", true, false, routing.Source{}, ""},
		{"a source with audio needs no tier", true, true, audio, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{}
			e.measured = tc.measured
			e.source = tc.src
			s := failoverOnSettings()
			s.Synth.SilenceOnVideoOnly = tc.on

			if got := e.wantSilence(s); got != tc.want {
				switch {
				case tc.want == "":
					t.Errorf("wantSilence = %q, want empty: a tier was raised for a "+
						"state that must not have one", got)
				case got == "":
					t.Errorf("wantSilence = empty, want the video-only tier: a "+
						"video-only ingest publishes no audio at all")
				default:
					t.Errorf("wantSilence = %q, want %q: the tier's signature drifted, "+
						"so every destination is compiled against a layout that no "+
						"longer describes what is on air", got, tc.want)
				}
			}
		})
	}
}
