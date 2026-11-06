package freedisk

import (
	"path/filepath"
	"testing"
)

// Callers treat total == 0 as "this platform cannot measure the volume" and
// decline to act. Windows used to return only that sentinel, which left the
// recorder's free-space guard dead there, so Space must report real figures.
func TestSpaceReportsRealVolumeFigures(t *testing.T) {
	free, total, err := Space(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if total == 0 {
		t.Fatal("total = 0 reads as 'unsupported platform'")
	}
	if free > total {
		t.Errorf("free = %d exceeds total = %d", free, total)
	}
}

func TestSpaceReportsErrorsRatherThanZeroes(t *testing.T) {
	free, total, err := Space(filepath.Join(t.TempDir(), "no", "such", "dir"))
	if err == nil {
		t.Fatal("a missing path measured without error")
	}
	if free != 0 || total != 0 {
		t.Errorf("got (%d, %d) alongside the error, want zeroes", free, total)
	}
	if _, _, err := Space("bad\x00path"); err == nil {
		t.Fatal("a path with NUL measured without error")
	}
}
