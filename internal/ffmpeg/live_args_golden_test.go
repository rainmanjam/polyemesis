package ffmpeg

import (
	"slices"
	"testing"
)

// The live argument lists for ORDINARY paths and text, pinned byte for byte.
//
// The filter escaper moved from one replacer that doubled the escape on every
// metacharacter to the two-level form FFmpeg's documentation gives (option
// level, then graph level). For '\' and ':' -- the only metacharacters an
// ordinary install's paths and text carry, Windows drive letters included --
// both forms produce the same bytes. This is the proof that nothing changed for
// anybody whose data directory, fonts and overlay text have none of
// ' , [ ] ; in them: these literals were captured from the builder BEFORE the
// escaper changed, and the escaper change left them alone.
//
// Literal forward-slash paths rather than t.TempDir or filepath.Join, so the
// strings are the same on every platform and every run.
func TestLiveArgsForOrdinaryPathsAreUnchanged(t *testing.T) {
	// logLevelArgs honours this, and the literals were captured without it.
	t.Setenv("POLYEMESIS_FFMPEG_LOGLEVEL", "")
	const fonts = "/var/lib/polyemesis/fonts/Inter-Regular.ttf"
	const winFonts = "C:/ProgramData/Poly Emesis/fonts/Inter-Regular.ttf"

	overlayAndText := baseRendition()
	overlayAndText.Width, overlayAndText.Height = 1920, 1080
	overlayAndText.Overlay = &OverlaySpec{
		ImagePath: "/var/lib/polyemesis/overlays/logo.png",
		Anchor:    AnchorTopRight, WidthPct: 0.15, MarginXPct: 0.02, MarginYPct: 0.02,
	}
	overlayAndText.Text = &TextSpec{
		Text: `LIVE 12:30 - 100% \o/`, FontFile: fonts, Anchor: AnchorBottomLeft,
		SizePct: 0.05, Color: "white@0.9", MarginXPct: 0.03, MarginYPct: 0.03,
		Box: true, BoxColor: "0x101010", BoxOpacity: 0.6,
	}

	captions := baseRendition()
	captions.Width, captions.Height = 1280, 720
	captions.Captions = &TextSpec{
		TextFile: "/var/lib/polyemesis/captions/burn-source-7.txt", FontFile: fonts,
		Anchor: AnchorBottomCenter, SizePct: 0.06, Color: "white", Box: true,
	}

	windows := baseRendition()
	windows.Width, windows.Height = 1280, 720
	windows.Text = &TextSpec{Text: "ON AIR", FontFile: winFonts, SizePct: 0.05, Color: "yellow"}
	windows.Captions = &TextSpec{
		TextFile: "C:/ProgramData/Poly Emesis/captions/burn-source-3.txt", FontFile: winFonts,
		SizePct: 0.05, Color: "white",
	}

	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"rendition with image overlay and boxed text", RenditionArgs(overlayAndText), goldenOverlayAndText},
		{"rendition burning live captions", RenditionArgs(captions), goldenCaptions},
		{"rendition with Windows drive-letter paths", RenditionArgs(windows), goldenWindows},
		{"colour slate", SlateArgs(SlateSpec{OutRelayURL: "udp://127.0.0.1:20020", Color: "0x112233@0.5"}), goldenColourSlate},
		{"image slate", SlateArgs(SlateSpec{
			OutRelayURL: "udp://127.0.0.1:20020", ImagePath: "/var/lib/polyemesis/slates/be right back.png",
			Color: "navy", Width: 1920, Height: 1080,
		}), goldenImageSlate},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Compared as a slice, not joined: ["-map", "0:a"] and ["-map\n0:a"]
			// join to the same string and are different commands.
			if !slices.Equal(c.args, c.want) {
				t.Errorf("the live argv changed for an ordinary configuration.\n--- got\n%q\n--- want\n%q", c.args, c.want)
			}
		})
	}
}
