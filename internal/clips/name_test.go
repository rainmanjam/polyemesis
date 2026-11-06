package clips

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A clip's filename is pinned byte for byte: the clips already on disk were
// written in this shape, and listing and retention read it back.
func TestReserveWritesTheRecorderStampShape(t *testing.T) {
	dir := t.TempDir()
	c := &Capturer{cfg: Config{Dir: dir}}
	at := time.Date(2026, 7, 27, 20, 30, 0, 0, time.Local)

	name, path, err := c.reserve(at)
	if err != nil || name != "clip-20260727-203000.ts" || path != filepath.Join(dir, name) {
		t.Fatalf("reserve = %q, %q, %v; want clip-20260727-203000.ts", name, path, err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if name, _, err := c.reserve(at); err != nil || name != "clip-20260727-203000-2.ts" {
		t.Fatalf("second reserve = %q, %v; want clip-20260727-203000-2.ts", name, err)
	}

	fallback := time.Unix(0, 0)
	for _, n := range []string{"clip-20260727-203000.ts", "clip-20260727-203000-2.ts"} {
		if got := startTimeFromName(n, fallback); !got.Equal(at) {
			t.Errorf("startTimeFromName(%q) = %v, want %v", n, got, at)
		}
	}
	if got := startTimeFromName("clip-garbage.ts", fallback); !got.Equal(fallback) {
		t.Errorf("startTimeFromName on a name with no stamp = %v, want the fallback", got)
	}
}
