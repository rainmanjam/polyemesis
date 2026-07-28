//go:build windows

package recording

import (
	"path/filepath"
	"testing"
)

// The free-space guard treats total == 0 as "this platform cannot measure the
// volume" and declines to halt. That sentinel used to be all Windows ever
// returned, so the guard was dead code there: the recorder would happily fill
// the system volume. These tests pin that diskFree reports real figures.

func TestDiskFreeReportsRealVolumeFigures(t *testing.T) {
	dir := t.TempDir()

	free, total, err := diskFree(dir)
	if err != nil {
		t.Fatalf("diskFree(%q) failed: %v", dir, err)
	}
	if total == 0 {
		t.Fatal("total = 0, which the free-space guard reads as 'unsupported platform' and ignores")
	}
	if free > total {
		t.Errorf("free = %d exceeds total = %d", free, total)
	}
}

func TestDiskFreeAcceptsEveryPathFormTheConfigCanHold(t *testing.T) {
	dir := t.TempDir()
	volume := filepath.VolumeName(dir) // "C:"

	tests := []struct {
		name string
		path string
	}{
		{"a plain directory path", dir},
		{"a directory path with a trailing separator", dir + `\`},
		{"a directory path with forward slashes", filepath.ToSlash(dir)},
		{"a bare drive letter, which means the volume and not the working directory", volume},
		{"a drive root", volume + `\`},
	}

	// Every form names the same volume, so every form must agree on its size.
	var want uint64
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			free, total, err := diskFree(tt.path)
			if err != nil {
				t.Fatalf("diskFree(%q) failed: %v", tt.path, err)
			}
			if total == 0 {
				t.Fatalf("diskFree(%q) reported a zero-byte volume", tt.path)
			}
			if free > total {
				t.Errorf("free = %d exceeds total = %d", free, total)
			}
			if want == 0 {
				want = total
				return
			}
			if total != want {
				t.Errorf("total = %d, want %d: every path form names the same volume", total, want)
			}
		})
	}
}

func TestDiskFreeReportsErrorsRatherThanZeroes(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"a directory on a drive that does not exist", `Q:\polyemesis-no-such-volume`},
		{"a path containing a NUL, which cannot be widened", "C:\\bad\x00path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			free, total, err := diskFree(tt.path)
			if err == nil {
				t.Fatalf("diskFree(%q) = (%d, %d, nil), want an error", tt.path, free, total)
			}
			// Zero *with* an error is fine; zero *without* one is the silent
			// failure that disables the guard.
			if free != 0 || total != 0 {
				t.Errorf("diskFree(%q) = (%d, %d), want zeroes alongside the error", tt.path, free, total)
			}
		})
	}
}
