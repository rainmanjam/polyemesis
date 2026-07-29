package db

import (
	"strings"
	"testing"
)

func textRendition(mut func(*RenditionText)) Rendition {
	r := Rendition{
		Name: "1080p", Width: 1920, Height: 1080, VideoBitrate: 6000,
		Encoder: EncoderX264, Preset: "veryfast", GOPSeconds: 2,
		Text: RenditionText{Content: "MY STATION", SizePct: 0.06, Color: "white"},
	}
	if mut != nil {
		mut(&r.Text)
	}
	return r
}

// The zero value must change nothing. Every rendition that predates these
// columns has to keep producing exactly the frame it always did.
func TestARenditionWithNoTextValidates(t *testing.T) {
	r := textRendition(func(tx *RenditionText) { *tx = RenditionText{} })
	if err := r.Validate(); err != nil {
		t.Fatalf("a rendition with no text was refused: %v", err)
	}
	if r.Text.Active() {
		t.Error("an empty text block reads as active")
	}
	// Half-filled geometry with no content is not text and must not be
	// validated as if it were -- otherwise the defaults a form sends before
	// anyone types would be refused.
	r = textRendition(func(tx *RenditionText) { *tx = RenditionText{SizePct: 99, Anchor: "nonsense"} })
	if err := r.Validate(); err != nil {
		t.Errorf("geometry with no text was validated as text: %v", err)
	}
}

// The rule that would otherwise be found as a caption that silently is not
// there: type is sized as a percentage of the output HEIGHT, so the height has
// to resolve to a number when the arguments are built.
func TestTextNeedsBothAxes(t *testing.T) {
	r := textRendition(nil)
	r.Width = 0
	err := r.Validate()
	if err == nil {
		t.Fatal("text on a rendition with a free axis was accepted")
	}
	if !strings.Contains(err.Error(), "width AND height") {
		t.Errorf("error was %q; it should say which setting is missing", err)
	}
}

func TestTextRejectsWhatWouldBreakTheFilterOrTheFrame(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*RenditionText)
		want string
	}{
		// A newline ends the filter argument; a NUL truncates the C string
		// FFmpeg receives. Both are rejected rather than escaped.
		{"a line break", func(x *RenditionText) { x.Content = "two\nlines" }, "single line"},
		{"a null byte", func(x *RenditionText) { x.Content = "a\x00b" }, "single line"},
		{"absurdly long", func(x *RenditionText) { x.Content = strings.Repeat("x", MaxTextLen+1) }, "longer than"},
		{"size below the floor", func(x *RenditionText) { x.SizePct = 0.0001 }, "out of range"},
		{"size above the ceiling", func(x *RenditionText) { x.SizePct = 0.9 }, "out of range"},
		{"a negative margin", func(x *RenditionText) { x.MarginXPct = -0.1 }, "out of range"},
		{"a margin past the cap", func(x *RenditionText) { x.MarginYPct = 0.9 }, "out of range"},
		{"an unknown anchor", func(x *RenditionText) { x.Anchor = "middle-ish" }, "unknown text anchor"},
		{"box opacity out of range", func(x *RenditionText) { x.BoxOpacity = 2 }, "out of range"},
		// The font is a bare filename. Both separators, because the check that
		// used the local one shipped twice in this codebase.
		{"a font with a slash", func(x *RenditionText) { x.Font = "sub/f.ttf" }, "bare filename"},
		{"a font with a backslash", func(x *RenditionText) { x.Font = `sub\f.ttf` }, "bare filename"},
		{"a font that climbs out", func(x *RenditionText) { x.Font = "../../secret.key" }, "bare filename"},
		// Colours land in a filter argument. Escaping protects them, but a
		// validator that accepts arbitrary punctuation is one escaping bug away
		// from letting a database row rewrite the filtergraph.
		{"a colour with a colon", func(x *RenditionText) { x.Color = "white:x=0" }, "colour name"},
		{"a colour with a comma", func(x *RenditionText) { x.Color = "white,drawbox" }, "colour name"},
		{"a box colour with a quote", func(x *RenditionText) { x.BoxColor = "black'" }, "colour name"},
		{"an alpha that is not a number", func(x *RenditionText) { x.Color = "white@opaque" }, "alpha"},
		{"an alpha out of range", func(x *RenditionText) { x.Color = "white@4" }, "alpha"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := textRendition(tc.mut).Validate()
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q, so it does not say what to fix", err, tc.want)
			}
		})
	}
}

// The positive cases. A validator that refuses everything passes every check
// above while making the feature unusable, which is the failure mode a
// negative-only table cannot see.
func TestTextAcceptsWhatItOffers(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*RenditionText)
	}{
		{"the defaults", nil},
		{"a percent sign, which is a glyph not a directive", func(x *RenditionText) { x.Content = "100% LIVE" }},
		{"a colon in the text", func(x *RenditionText) { x.Content = "Starts at 19:30" }},
		{"an apostrophe", func(x *RenditionText) { x.Content = "Tom's show" }},
		{"a bare font name", func(x *RenditionText) { x.Font = "MyStation.ttf" }},
		{"no font, meaning the built-in", func(x *RenditionText) { x.Font = "" }},
		{"a hex colour", func(x *RenditionText) { x.Color = "0x101010" }},
		{"a colour with an alpha", func(x *RenditionText) { x.Color = "white@0.5" }},
		{"a box", func(x *RenditionText) { x.Box, x.BoxColor, x.BoxOpacity = true, "black", 0.5 }},
		{"the size floor exactly", func(x *RenditionText) { x.SizePct = MinTextSizePct }},
		{"the size ceiling exactly", func(x *RenditionText) { x.SizePct = MaxTextSizePct }},
		{"the margin cap exactly", func(x *RenditionText) { x.MarginXPct = MaxTextMarginPct }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := textRendition(tc.mut).Validate(); err != nil {
				t.Errorf("%s was refused: %v", tc.name, err)
			}
		})
	}
	// Every anchor the UI offers must validate, or the picker has entries that
	// cannot be saved.
	for _, a := range []string{
		"top-left", "top-center", "top-right",
		"middle-left", "center", "middle-right",
		"bottom-left", "bottom-center", "bottom-right",
	} {
		if err := textRendition(func(x *RenditionText) { x.Anchor = a }).Validate(); err != nil {
			t.Errorf("anchor %s is offered and refused: %v", a, err)
		}
	}
}

// Text and an image overlay are independent. Either, both or neither.
func TestTextAndOverlayAreIndependent(t *testing.T) {
	r := textRendition(nil)
	r.Overlay = RenditionOverlay{Image: "overlays/logo.png", WidthPct: 0.2, Opacity: 1}
	if err := r.Validate(); err != nil {
		t.Errorf("a rendition with both a logo and a caption was refused: %v", err)
	}
	if !r.Text.Active() || !r.Overlay.Active() {
		t.Error("one of the two reads as inactive when both are set")
	}
}

// The plumbing, which is where a 31-placeholder INSERT goes wrong SILENTLY.
//
// A misordered column list still executes: SQLite takes the values positionally
// and the row comes back with the box colour in the anchor field and no error
// anywhere. Every field is given a DISTINCT value so a swap cannot pass by
// coincidence -- two fields that both default to "" would round-trip fine while
// transposed.
func TestTextRoundTripsEveryFieldThroughTheStore(t *testing.T) {
	d := testDB(t)

	want := validRendition()
	want.Width, want.Height = 1920, 1080
	want.Text = RenditionText{
		Content:    "MY STATION",
		Font:       "MyStation.ttf",
		Anchor:     "top-right",
		SizePct:    0.07,
		Color:      "0x102030",
		MarginXPct: 0.03,
		MarginYPct: 0.11,
		Box:        true,
		BoxColor:   "0x405060",
		BoxOpacity: 0.42,
	}

	created := mustCreateRendition(t, d, want)
	got, err := d.GetRendition(created.ID)
	if err != nil {
		t.Fatalf("GetRendition: %v", err)
	}
	if got.Text != want.Text {
		t.Errorf("text round trip =\n %+v\nwant\n %+v", got.Text, want.Text)
	}

	// And through an update, which is a separate column list and therefore a
	// separate chance to transpose two of them.
	got.Text.Content = "CHANGED"
	got.Text.Anchor = "bottom-center"
	got.Text.Box = false
	updated, err := d.UpdateRendition(got)
	if err != nil {
		t.Fatalf("UpdateRendition: %v", err)
	}
	if updated.Text.Content != "CHANGED" || updated.Text.Anchor != "bottom-center" || updated.Text.Box {
		t.Errorf("update did not persist the text: %+v", updated.Text)
	}
	// The fields NOT touched by the update must survive it.
	if updated.Text.Font != want.Text.Font || updated.Text.BoxColor != want.Text.BoxColor ||
		updated.Text.SizePct != want.Text.SizePct {
		t.Errorf("an update blanked fields it did not change: %+v", updated.Text)
	}
	// The overlay must be untouched by any of this.
	if updated.Overlay != want.Overlay {
		t.Errorf("the text columns disturbed the overlay ones: %+v", updated.Overlay)
	}
}
