//go:build windows

package freedisk

import (
	"path/filepath"
	"testing"
)

// Every path form the config can hold names one volume, so every form must
// agree on its size.
func TestSpaceAcceptsEveryPathFormTheConfigCanHold(t *testing.T) {
	dir := t.TempDir()
	volume := filepath.VolumeName(dir)
	var want uint64
	for _, path := range []string{dir, dir + `\`, filepath.ToSlash(dir), volume, volume + `\`} {
		free, total, err := Space(path)
		if err != nil {
			t.Fatalf("Space(%q): %v", path, err)
		}
		if total == 0 || free > total {
			t.Fatalf("Space(%q) = (%d, %d)", path, free, total)
		}
		if want == 0 {
			want = total
		} else if total != want {
			t.Errorf("Space(%q) total = %d, want %d", path, total, want)
		}
	}
}
