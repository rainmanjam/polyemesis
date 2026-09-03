package db

import (
	"strings"
	"testing"
)

// #661. The catalogue is a snapshot of somebody else's documentation, so these
// pin the COMPARISON and the evidence it carries -- never the figures
// themselves, which change without notice and belong to platforms.go.

func guidedPreset(t *testing.T) (DestinationPreset, VideoGuidance) {
	t.Helper()
	for _, p := range DestinationPresets() {
		if p.Video != nil && p.Video.KbpsMax > 0 && p.Video.Height > 0 {
			return p, *p.Video
		}
	}
	// FATAL, NOT SKIP. platforms.go is committed data, not an environment: if no
	// preset publishes a height and a bitrate any more, the comparison this file
	// tests has nothing left to compare and the feature is dead. Skipping would
	// print ok and count as coverage -- the exact free pass the skip census
	// exists to remove.
	t.Fatal("no preset publishes both a height and a bitrate ceiling, so " +
		"RenditionConcerns can never fire. platforms.go is in this repository: if " +
		"that is now true it is a regression in the catalogue, not a reason to " +
		"stop testing.")
	return DestinationPreset{}, VideoGuidance{}
}

func TestARenditionInsideTheGuidanceRaisesNothing(t *testing.T) {
	p, g := guidedPreset(t)
	r := &Rendition{Height: g.Height, FPS: g.FPS, VideoBitrate: g.KbpsMax}
	if got := RenditionConcerns(r, Platform(p.ID)); len(got) != 0 {
		t.Fatalf("a rendition exactly at %s's published figures raised %d concerns: %+v\n\n"+
			"Warning at the published limit trains the operator to ignore the warning.",
			p.Name, len(got), got)
	}
}

func TestAnOverBitrateRenditionIsFlaggedWithItsEvidence(t *testing.T) {
	p, g := guidedPreset(t)
	r := &Rendition{Height: g.Height, VideoBitrate: g.KbpsMax * 3}
	got := RenditionConcerns(r, Platform(p.ID))
	if len(got) == 0 {
		t.Fatalf("a rendition at 3x %s's published ceiling raised nothing.\n\n"+
			"This is not refused at configure time -- it is accepted, encoded, "+
			"published and dropped by the platform mid-broadcast, with nothing in the "+
			"console pointing at the bitrate.", p.Name)
	}
	c := got[0]
	if c.Source == "" || c.Checked == "" {
		t.Errorf("concern %+v carries no Source/Checked.\n\n"+
			"A warning an operator cannot check is one they can only obey or ignore. "+
			"X's own two pages disagree materially, so the date and the URL are what "+
			"let them judge whether the catalogue or their choice is stale.", c)
	}
}

// The rate-control CEILING is what the platform sees, not the target.
func TestTheMaxrateIsWhatIsComparedWhenItIsHigher(t *testing.T) {
	p, g := guidedPreset(t)
	r := &Rendition{VideoBitrate: g.KbpsMax, MaxrateKbps: g.KbpsMax * 2}
	if got := RenditionConcerns(r, Platform(p.ID)); len(got) == 0 {
		t.Fatalf("a rendition whose maxrate is twice %s's ceiling raised nothing; "+
			"the peak is what the platform actually receives", p.Name)
	}
}

// 0 means "keep the source's", and the source is unknown until something is
// streaming. Guessing would produce a warning about a figure nobody chose.
func TestUnsetRenditionFieldsAreNotJudged(t *testing.T) {
	p, _ := guidedPreset(t)
	r := &Rendition{VideoBitrate: 1} // width, height, fps, gop all unset
	for _, c := range RenditionConcerns(r, Platform(p.ID)) {
		if c.Field == "width" || c.Field == "height" || c.Field == "fps" || c.Field == "gop" {
			t.Errorf("judged %q on a rendition that leaves it to the source: %s", c.Field, c.Detail)
		}
	}
}

// Eleven of thirty-three presets publish guidance. The rest are "no opinion",
// which must not read as "no problem" by accident -- it reads as silence
// because there is genuinely nothing to say.
func TestAPlatformWithNoPublishedGuidanceSaysNothing(t *testing.T) {
	var bare string
	for _, p := range DestinationPresets() {
		if p.Video == nil {
			bare = p.ID
			break
		}
	}
	if bare == "" {
		// Every preset publishing guidance would be good news, and would make
		// this case unreachable -- but it is committed data, so say so rather
		// than printing ok.
		t.Fatal("every preset now publishes guidance, so the 'no opinion' path is " +
			"unreachable. Delete this test deliberately rather than letting it skip.")
	}
	r := &Rendition{Width: 7680, Height: 4320, FPS: 120, VideoBitrate: 99000}
	if got := RenditionConcerns(r, Platform(bare)); len(got) != 0 {
		t.Fatalf("preset %q publishes no guidance yet produced %d concerns: %+v", bare, len(got), got)
	}
	if got := RenditionConcerns(r, Platform("no-such-platform")); len(got) != 0 {
		t.Fatalf("an unknown platform produced %d concerns", len(got))
	}
	if got := RenditionConcerns(nil, Platform(bare)); got != nil {
		t.Fatal("a nil rendition produced concerns")
	}
}

// A single published figure is not a range, and must not be described as one.
func TestASinglePublishedFigureIsNotDescribedAsARange(t *testing.T) {
	for _, p := range DestinationPresets() {
		if p.Video == nil || p.Video.KbpsMax == 0 || p.Video.KbpsMin != p.Video.KbpsMax {
			continue
		}
		r := &Rendition{VideoBitrate: p.Video.KbpsMax * 2}
		got := RenditionConcerns(r, Platform(p.ID))
		if len(got) == 0 {
			t.Fatalf("%s publishes a single figure and an over-rate rendition raised nothing", p.Name)
		}
		if strings.Contains(got[0].Detail, "up to") {
			t.Fatalf("%s publishes ONE figure (%d) and the warning calls it a range: %q",
				p.Name, p.Video.KbpsMax, got[0].Detail)
		}
		return
	}
	t.Fatal("no preset publishes a single bitrate figure (KbpsMin == KbpsMax), so " +
		"the single-figure wording is untested. X published exactly that shape when " +
		"#661 was written; if it no longer does, check the catalogue rather than " +
		"dropping the assertion.")
}

// The keyframe interval is the one figure platforms enforce by degrading rather
// than rejecting, so it is the easiest to get wrong and never notice.
func TestALongerKeyframeIntervalThanThePlatformAsksForIsFlagged(t *testing.T) {
	for _, p := range DestinationPresets() {
		if p.Video == nil || p.Video.GOPSeconds <= 0 {
			continue
		}
		r := &Rendition{GOPSeconds: p.Video.GOPSeconds * 3}
		got := RenditionConcerns(r, Platform(p.ID))
		var found bool
		for _, c := range got {
			if c.Field == "gop" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s asks for a %gs keyframe interval and a %gs rendition raised no "+
				"gop concern: %+v", p.Name, p.Video.GOPSeconds, r.GOPSeconds, got)
		}
		// And an interval inside the ask must stay silent.
		if got := RenditionConcerns(&Rendition{GOPSeconds: p.Video.GOPSeconds}, Platform(p.ID)); len(got) != 0 {
			t.Fatalf("%s raised a concern for a rendition exactly at its published "+
				"keyframe interval: %+v", p.Name, got)
		}
		return
	}
	t.Fatal("no preset publishes a keyframe interval, so the gop comparison is " +
		"untested. platforms.go is committed data: if that is now true, check the " +
		"catalogue rather than dropping the assertion.")
}

// Below the published floor is a different failure from above the ceiling: the
// stream is accepted and looks bad, rather than being dropped.
func TestABitrateBelowThePublishedFloorIsFlagged(t *testing.T) {
	for _, p := range DestinationPresets() {
		if p.Video == nil || p.Video.KbpsMin <= 1 || p.Video.KbpsMin == p.Video.KbpsMax {
			continue
		}
		r := &Rendition{VideoBitrate: p.Video.KbpsMin / 2}
		got := RenditionConcerns(r, Platform(p.ID))
		if len(got) == 0 {
			t.Fatalf("%s publishes at least %d kbps and half that raised nothing",
				p.Name, p.Video.KbpsMin)
		}
		if !strings.Contains(got[0].Detail, "at least") {
			t.Fatalf("an under-floor bitrate is described as %q; it is a floor, not a "+
				"ceiling, and the two are different failures", got[0].Detail)
		}
		return
	}
	t.Fatal("no preset publishes a bitrate RANGE (KbpsMin < KbpsMax), so the floor " +
		"comparison is untested. Check the catalogue rather than dropping this.")
}
