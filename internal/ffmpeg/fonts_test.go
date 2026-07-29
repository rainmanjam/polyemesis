package ffmpeg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureFontsWritesTheBuiltInsAndIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), FontsDirName)
	if err := EnsureFonts(dir); err != nil {
		t.Fatalf("EnsureFonts: %v", err)
	}
	for _, name := range BuiltinFonts {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s was not written: %v", name, err)
		}
		// A font that exists but is empty is worse than one that is missing:
		// the failure surfaces at stream time as a FreeType error.
		if st.Size() < 50_000 {
			t.Errorf("%s is %d bytes, which is not a usable TrueType file", name, st.Size())
		}
	}
	// OFL 1.1 requires the copyright notice and licence to travel with the
	// font. Shipping the bytes without it would be a licence violation, so it
	// is asserted rather than assumed.
	lic, err := os.ReadFile(filepath.Join(dir, "LICENSE-Inter.txt"))
	if err != nil {
		t.Fatalf("the licence was not written alongside the fonts: %v", err)
	}
	for _, want := range []string{
		"The Inter project authors",
		"SIL OPEN FONT LICENSE Version 1.1",
	} {
		if !strings.Contains(string(lic), want) {
			t.Errorf("the licence file does not contain %q", want)
		}
	}

	// Idempotent: a second call must not fail, and must leave the same bytes.
	before, _ := os.ReadFile(filepath.Join(dir, DefaultFont))
	if err := EnsureFonts(dir); err != nil {
		t.Fatalf("second EnsureFonts: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, DefaultFont))
	if string(before) != string(after) {
		t.Error("a second EnsureFonts changed the font file")
	}
}

// A built-in that drifted must be repaired, because the failure it causes
// otherwise happens at stream time and names FreeType rather than anything an
// operator can act on.
func TestEnsureFontsRepairsATruncatedBuiltIn(t *testing.T) {
	dir := filepath.Join(t.TempDir(), FontsDirName)
	if err := EnsureFonts(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, DefaultFont)
	if err := os.WriteFile(path, []byte("truncated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureFonts(dir); err != nil {
		t.Fatalf("EnsureFonts: %v", err)
	}
	if st, _ := os.Stat(path); st.Size() < 50_000 {
		t.Errorf("a truncated built-in was left at %d bytes", st.Size())
	}
}

// An operator's own font must survive a restart. The built-ins are rewritten on
// every startup, and a rule that rewrote everything would delete the font
// somebody installed.
func TestEnsureFontsLeavesAnOperatorFontAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), FontsDirName)
	if err := EnsureFonts(dir); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(dir, "MyStation.ttf")
	if err := os.WriteFile(mine, []byte("operator supplied"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureFonts(dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(mine)
	if err != nil || string(got) != "operator supplied" {
		t.Errorf("the operator's own font was modified or removed: %q, %v", got, err)
	}
}

func TestListFontsShowsBuiltInsAndOperatorFonts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), FontsDirName)
	if err := EnsureFonts(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MyStation.otf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Not a font, and must not be offered: the picker showing it would produce
	// a selection that fails at stream time.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ListFonts(dir)
	if err != nil {
		t.Fatalf("ListFonts: %v", err)
	}
	joined := strings.Join(got, ",")
	for _, want := range append(append([]string{}, BuiltinFonts...), "MyStation.otf") {
		if !strings.Contains(joined, want) {
			t.Errorf("%s missing from %v", want, got)
		}
	}
	if strings.Contains(joined, "notes.txt") {
		t.Errorf("a non-font was offered: %v", got)
	}
}

// A missing directory is "no fonts", not a failure: callers ask before startup
// has created it.
func TestListFontsOnAMissingDirectoryIsEmptyNotAnError(t *testing.T) {
	got, err := ListFonts(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Errorf("ListFonts on a missing directory failed: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}

func TestFontPathRefusesAnythingThatIsNotABareFontName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), FontsDirName)
	if err := EnsureFonts(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"", ".", "..",
		"../../etc/passwd",
		"sub/Inter-Regular.ttf",
		"/etc/Inter-Regular.ttf",
		// Backslash cases, which are what make this rule testable on Linux.
		// A backslash is a legal POSIX filename character, so these fail here
		// if the check ever narrows to the local separator -- the exact
		// mistake internal/recording shipped and Windows CI found.
		`sub\Inter-Regular.ttf`,
		`..\..\secret.key`,
		// Real file, wrong kind: handing an arbitrary file to FreeType is not
		// something to do on an operator's typo.
		"LICENSE-Inter.txt",
		// Shaped like a font but absent, which must be refused at save time
		// rather than at 3am.
		"NotInstalled.ttf",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := FontPath(dir, name); err == nil {
				t.Errorf("FontPath(%q) = %q, want an error", name, got)
			}
		})
	}

	// The positive case. A rule that refuses everything passes every check
	// above while making the feature unusable.
	got, err := FontPath(dir, DefaultFont)
	if err != nil {
		t.Fatalf("FontPath(%q): %v", DefaultFont, err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("FontPath returned %q, which is not absolute; drawtext needs a real path", got)
	}
}

// THE test this whole file exists for.
//
// Everything above proves bytes land on disk. This proves the thing that
// actually matters and that no unit test can: that FFmpeg renders text with the
// embedded font in an environment with NO SYSTEM FONTS.
//
// The shipping Alpine image is exactly that environment -- fontconfig installed
// and nothing for it to find -- so `drawtext=text=hi` fails there with "Cannot
// find a valid font for the family Sans" while working on any developer laptop.
// Run it with ./scripts/test-in-docker.sh, which is that image.
//
// Measured by comparing PIXELS rather than by checking FFmpeg's exit code. A
// drawtext that silently drew nothing exits 0, and asserting on the status
// would call that a pass.
func TestDrawtextRendersWithTheEmbeddedFont(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	tools := &Tools{FFmpeg: bin}
	tools.checkFilters(context.Background())
	if !tools.HasFilter("drawtext") {
		// A skip with the reason, and the way to run it for real. A Homebrew
		// FFmpeg on macOS is built without libfreetype and has no drawtext at
		// all, so this skips on the machine most of this was written on.
		t.Skip("this FFmpeg has no drawtext (built without libfreetype); " +
			"run ./scripts/test-in-docker.sh to test against the image that ships")
	}

	dir := filepath.Join(t.TempDir(), FontsDirName)
	if err := EnsureFonts(dir); err != nil {
		t.Fatal(err)
	}
	font, err := FontPath(dir, DefaultFont)
	if err != nil {
		t.Fatal(err)
	}

	// The font path goes into a FILTER argument, where ':' and '\' are
	// metacharacters. On Windows the path is C:\...\Inter-Regular.ttf and
	// carries both, so an unescaped path is not merely wrong there -- it is a
	// parse error. escapeLavfiValue is what the rest of this package uses.
	frame := func(text string) string {
		out := filepath.Join(t.TempDir(), "f.png")
		graph := "color=c=black:s=320x120:d=1"
		if text != "" {
			graph += ",drawtext=fontfile=" + escapeLavfiValue(font) +
				":text=" + text + ":fontcolor=white:fontsize=48:x=10:y=30"
		}
		cmd := exec.Command(bin, "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", graph, "-frames:v", "1", "-y", out)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg failed for text=%q: %v\n%s", text, err, b)
		}
		return out
	}

	blank := nonBlackPixels(t, bin, frame(""))
	drawn := nonBlackPixels(t, bin, frame("POLYEMESIS"))

	if blank != 0 {
		t.Fatalf("the control frame has %d non-black pixels, so the measurement is not sound", blank)
	}
	// A real glyph run at 48px covers thousands of pixels. A low threshold
	// rather than an exact count, because font rasterisation legitimately
	// differs between FreeType versions -- what must not differ is whether
	// anything was drawn at all.
	if drawn < 500 {
		t.Errorf("drawtext produced %d non-black pixels; the font did not render", drawn)
	}
	t.Logf("embedded font rendered %d non-black pixels (control: %d)", drawn, blank)
}

// nonBlackPixels counts lit pixels by re-reading the frame through FFmpeg,
// so the assertion is about what was actually encoded rather than about what
// the filter graph claimed.
func nonBlackPixels(t *testing.T, bin, png string) int {
	t.Helper()
	// gray + rawvideo gives one byte per pixel on stdout, which needs no image
	// decoder in the test and cannot disagree with one.
	cmd := exec.Command(bin, "-hide_banner", "-loglevel", "error",
		"-i", png, "-pix_fmt", "gray", "-f", "rawvideo", "-")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("reading %s back: %v", png, err)
	}
	n := 0
	for _, b := range out {
		// Not > 0: JPEG-ish ringing and rounding leave near-black noise even on
		// a synthetic source. 32 is well below white text and well above it.
		if b > 32 {
			n++
		}
	}
	return n
}
