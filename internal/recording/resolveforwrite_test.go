package recording

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These cover the rule a file destination depends on to survive a restart.
//
// The bug they exist for: FFmpeg refuses an existing output file and exits, so
// a destination writing to a fixed name worked exactly once. Every respawn
// after that died with "already exists", the destination crash-looped, and the
// operator was left with a zero-byte recording and a process list full of
// retries. It took an end-to-end run to see it, because every unit test in
// the package resolved a name without ever asking whether FFmpeg would accept
// it.

func TestResolveForWriteUsesTheConfiguredNameWhenFree(t *testing.T) {
	m, dir, _ := newManagerIn(t, t.TempDir())

	got, err := m.ResolveForWrite("show.mkv")
	if err != nil {
		t.Fatalf("ResolveForWrite: %v", err)
	}
	if want := filepath.Join(dir, "show.mkv"); got != want {
		t.Fatalf("got %q, want the configured name %q", got, want)
	}
}

func TestResolveForWriteClearsAnEmptyLeftover(t *testing.T) {
	// The exact production case: a previous attempt created the file and died
	// before writing a byte. Returning the path without removing it would hand
	// FFmpeg a path it refuses, which is how the destination stayed wedged.
	m, dir, _ := newManagerIn(t, t.TempDir())
	path := filepath.Join(dir, "show.mkv")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := m.ResolveForWrite("show.mkv")
	if err != nil {
		t.Fatalf("ResolveForWrite: %v", err)
	}
	if got != path {
		t.Fatalf("an empty leftover should be reused, got %q", got)
	}
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Fatal("the empty file must be REMOVED, not merely reported as usable: " +
			"FFmpeg refuses any path that exists")
	}
}

func TestResolveForWriteNeverDestroysFootage(t *testing.T) {
	// The trade this whole function exists to make. Passing -y instead would
	// have been one character and would have truncated a recording every time
	// the ingest hiccuped.
	m, dir, _ := newManagerIn(t, t.TempDir())
	path := filepath.Join(dir, "show.mkv")
	content := []byte("pretend this is an hour of broadcast")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := m.ResolveForWrite("show.mkv")
	if err != nil {
		t.Fatalf("ResolveForWrite: %v", err)
	}
	if got == path {
		t.Fatal("returned the path that already holds footage; writing there destroys it")
	}
	if kept, err := os.ReadFile(path); err != nil || string(kept) != string(content) {
		t.Fatalf("the existing recording was altered: err=%v", err)
	}
	// Still a sibling in the same directory, with the same extension, so the
	// operator finds it where they expect and it plays in the same player.
	if filepath.Dir(got) != dir || filepath.Ext(got) != ".mkv" {
		t.Fatalf("the new name should be a sibling with the same extension, got %q", got)
	}
	if !strings.HasPrefix(filepath.Base(got), "show-") {
		t.Fatalf("the new name should be recognisably derived from the old, got %q", got)
	}
}

func TestResolveForWriteSurvivesACrashLoopInsideOneSecond(t *testing.T) {
	// The timestamp only has seconds resolution, and a destination that fails
	// immediately restarts several times a second. Without the counter the
	// second call would hand back a path that the first call is already
	// writing to, and the two processes would fight over one file.
	m, dir, _ := newManagerIn(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(dir, "show.mkv"), []byte("footage"), 0o644); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		got, err := m.ResolveForWrite("show.mkv")
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if seen[got] {
			t.Fatalf("attempt %d returned %q again; two processes would write one file", i, got)
		}
		seen[got] = true
		// Simulate the respawn actually writing something, which is what makes
		// the name unavailable to the next one.
		if err := os.WriteFile(got, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolveForWriteStillRefusesAnEscapingName(t *testing.T) {
	// It must not be a way around Resolve's confinement.
	m, _, _ := newManagerIn(t, t.TempDir())
	for _, name := range []string{"../escape.mkv", "sub/dir.mkv", "", ".."} {
		if _, err := m.ResolveForWrite(name); err == nil {
			t.Fatalf("%q was accepted; it must be confined to the recordings directory", name)
		}
	}
}
