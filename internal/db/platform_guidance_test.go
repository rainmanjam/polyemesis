package db

import (
	"strings"
	"testing"
	"time"
)

/* Provenance is the whole contract of VideoGuidance.
 *
 * This catalogue's disclaimer says its numbers are a starting point that move
 * without notice. That is only honest while every number can be traced to the
 * platform that published it. A bitrate with no source is indistinguishable
 * from a guess once it is sitting in a form field, and the operator finds out
 * during a broadcast. */

func TestEveryVideoGuidanceCitesItsSource(t *testing.T) {
	for _, p := range DestinationPresets() {
		if p.Video == nil {
			continue // "not published" is a valid answer and ships as nothing
		}
		if strings.TrimSpace(p.Video.Source) == "" {
			t.Errorf("%s: video guidance with no Source. A number nobody can check is worse than no number", p.ID)
		}
		if !strings.HasPrefix(p.Video.Source, "https://") {
			t.Errorf("%s: Source %q is not a URL", p.ID, p.Video.Source)
		}
		if strings.TrimSpace(p.Video.Checked) == "" {
			t.Errorf("%s: video guidance with no Checked date", p.ID)
		}
		if _, err := time.Parse("2006-01-02", p.Video.Checked); err != nil {
			t.Errorf("%s: Checked %q is not YYYY-MM-DD", p.ID, p.Video.Checked)
		}
	}
}

func TestVideoGuidanceRangesAreCoherent(t *testing.T) {
	for _, p := range DestinationPresets() {
		v := p.Video
		if v == nil {
			continue
		}
		if v.KbpsMin < 0 || v.KbpsMax < 0 {
			t.Errorf("%s: negative bitrate", p.ID)
		}
		if v.KbpsMin > 0 && v.KbpsMax > 0 && v.KbpsMin > v.KbpsMax {
			t.Errorf("%s: bitrate range inverted (%d..%d)", p.ID, v.KbpsMin, v.KbpsMax)
		}
		// A width with no height cannot be scaled to, and the aspect controls
		// need both — see RENDITIONS.md. Publishing one without the other would
		// seed a form that cannot be saved.
		if (v.Width > 0) != (v.Height > 0) {
			t.Errorf("%s: %dx%d — a guidance size needs both axes or neither", p.ID, v.Width, v.Height)
		}
		if v.FPS < 0 || v.FPS > 240 {
			t.Errorf("%s: implausible fps %d", p.ID, v.FPS)
		}
	}
}

// An unsupported platform must not carry encoder guidance: it says polyemesis
// cannot stream there, and a recommended bitrate beside that is a contradiction
// the operator has to resolve themselves.
func TestUnsupportedPresetsCarryNoGuidance(t *testing.T) {
	for _, p := range DestinationPresets() {
		if p.Kind == "" && p.Video != nil {
			t.Errorf("%s: has video guidance but no transport polyemesis can publish over", p.ID)
		}
	}
}
