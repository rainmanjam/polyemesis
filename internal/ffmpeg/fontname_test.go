package ffmpeg

import (
	"os"
	"path/filepath"
	"testing"
)

// libass picks a font by FAMILY name, not by file, so a caption style that
// names a file in the fonts directory has to be turned into the family and the
// weight the file declares. Read from the font's own name and OS/2 tables.
func TestFontFamilyReadsTheFontsOwnNames(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureFonts(dir); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]struct {
		family string
		bold   bool
	}{
		"Inter-Regular.ttf": {"Inter", false},
		"Inter-Bold.ttf":    {"Inter", true},
	} {
		fam, bold, err := FontFamily(filepath.Join(dir, file))
		if err != nil || fam != want.family || bold != want.bold {
			t.Errorf("%s: family %q bold %v (%v), want %q %v", file, fam, bold, err, want.family, want.bold)
		}
	}
	junk := filepath.Join(dir, "junk.ttf")
	_ = os.WriteFile(junk, []byte("not a font at all"), 0o644)
	if _, _, err := FontFamily(junk); err == nil {
		t.Error("a file that is not a font was read as one")
	}
}
