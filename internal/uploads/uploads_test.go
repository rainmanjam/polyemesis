package uploads

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// Resolve is the confinement boundary. Every case here is a path that must NOT
// resolve, and the positive case at the end matters just as much: a confinement
// test that only tries traversals passes just as happily when the feature is
// broken and refuses everything.
func TestResolveRefusesEscapes(t *testing.T) {
	s := newStore(t)
	bad := []string{
		"", ".", "..",
		"../etc/passwd",
		"..\\windows\\system32",
		"sub/dir.mp4",
		`sub\dir.mp4`,
		"/etc/passwd",
		`C:\Windows\system32`,
		"/absolute.mp4",
	}
	for _, name := range bad {
		t.Run("refuse "+name, func(t *testing.T) {
			if got, err := s.Resolve(name); err == nil {
				t.Fatalf("Resolve(%q) = %q, want an error", name, got)
			}
		})
	}
}

func TestResolveAcceptsABareName(t *testing.T) {
	s := newStore(t)
	got, err := s.Resolve("show-a1b2c3d4.mp4")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if filepath.Dir(got) != s.Dir() {
		t.Fatalf("resolved outside the uploads dir: %q", got)
	}
}

// Both separators are checked on EVERY platform, not just the local one. A
// check written as os.PathSeparator changes meaning with GOOS: on Windows that
// constant is '\', so "a/b" passed validation and Join produced a subdirectory
// path. internal/recording carried exactly that bug.
func TestResolveChecksBothSeparatorsOnEveryPlatform(t *testing.T) {
	s := newStore(t)
	for _, name := range []string{"a/b.mp4", `a\b.mp4`} {
		if _, err := s.Resolve(name); err == nil {
			t.Fatalf("Resolve(%q) succeeded; separator checks must not depend on GOOS", name)
		}
	}
}

// The client's filename is a hint. These assert it cannot become a path, an
// extension it should not have, or a name that collides with an existing file.
func TestSafeNameDiscardsTheClientsPath(t *testing.T) {
	cases := []struct {
		hint      string
		wantExt   string
		wantNoSep bool
	}{
		{"../../etc/passwd", ".bin", true},
		{`..\..\windows\system32\evil.exe`, ".bin", true},
		{"/tmp/show.mp4", ".mp4", true},
		{"perfectly normal.mkv", ".mkv", true},
		{"shell; rm -rf /.ts", ".ts", true},
		{"payload.php", ".bin", true},
		{"script.sh", ".bin", true},
		{"", ".bin", true},
		{"...", ".bin", true},
		{strings.Repeat("x", 400) + ".mp4", ".mp4", true},
	}
	for _, c := range cases {
		t.Run(c.hint, func(t *testing.T) {
			got := SafeName(c.hint)
			if strings.ContainsAny(got, `/\`) {
				t.Fatalf("SafeName(%q) = %q, contains a separator", c.hint, got)
			}
			if filepath.Ext(got) != c.wantExt {
				t.Fatalf("SafeName(%q) ext = %q, want %q", c.hint, filepath.Ext(got), c.wantExt)
			}
			if got == "" || got == "." || got == ".." {
				t.Fatalf("SafeName(%q) = %q", c.hint, got)
			}
			// The EXACT budget rather than a round number with slack in it:
			// the capped stem, one dash, the hex suffix, the extension. It
			// was MaxNameLength+16, which happened to fit a four-byte suffix
			// and silently became the thing that failed when the suffix grew
			// to eight (see nameSuffixBytes) -- a limit that moves when an
			// unrelated constant does is not a limit anyone can reason about.
			// Every filesystem this runs on takes 255 bytes per component and
			// this is 117.
			if max := MaxNameLength + 1 + 2*nameSuffixBytes + len(c.wantExt); len(got) > max {
				t.Fatalf("SafeName(%q) is %d chars, want at most %d", c.hint, len(got), max)
			}
		})
	}
}

// Two uploads of the same filename must not collide. Without this an operator
// could overwrite an existing upload by uploading a file of the same name --
// and so could anyone who guessed one.
func TestSafeNameIsUniquePerCall(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		n := SafeName("show.mp4")
		if seen[n] {
			t.Fatalf("SafeName collided after %d calls: %q", i, n)
		}
		seen[n] = true
	}
}

func TestSaveWritesAndLists(t *testing.T) {
	s := newStore(t)
	f, err := s.Save(strings.NewReader("some media bytes"), "My Show.mp4", 0, 0)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if f.Bytes != 16 {
		t.Fatalf("Bytes = %d, want 16", f.Bytes)
	}
	if !strings.HasSuffix(f.Name, ".mp4") {
		t.Fatalf("Name = %q, want a .mp4 suffix", f.Name)
	}
	// The pull URL is what an operator pastes into a pull source, so it has to
	// be relative to the data directory and forward-slashed on every platform.
	if f.PullURL != "file://"+Dir+"/"+f.Name {
		t.Fatalf("PullURL = %q", f.PullURL)
	}
	if strings.Contains(f.PullURL, `\`) {
		t.Fatalf("PullURL contains a backslash: %q", f.PullURL)
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != f.Name {
		t.Fatalf("List = %+v, want the one saved file", list)
	}
}

// An oversized body must leave NOTHING behind. A partial video is not visibly
// broken in a listing, so the operator would find out when the broadcast they
// scheduled goes to air.
func TestSaveRefusesOversizeAndLeavesNoPartial(t *testing.T) {
	s := newStore(t)
	_, err := s.Save(strings.NewReader(strings.Repeat("x", 5000)), "big.mp4", 1000, 0)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	assertDirEmpty(t, s)
}

func TestSaveRefusesEmpty(t *testing.T) {
	s := newStore(t)
	if _, err := s.Save(strings.NewReader(""), "nothing.mp4", 0, 0); !errors.Is(err, ErrEmpty) {
		t.Fatalf("err = %v, want ErrEmpty", err)
	}
	assertDirEmpty(t, s)
}

// A read that fails midway is a cancelled upload, and must also leave nothing.
func TestSaveCleansUpAfterAReadError(t *testing.T) {
	s := newStore(t)
	r := &failingReader{after: 8}
	if _, err := s.Save(r, "half.mp4", 0, 0); err == nil {
		t.Fatal("Save succeeded on a failing reader")
	}
	assertDirEmpty(t, s)
}

// Disk space is checked BEFORE the write, not discovered during it. A recorder
// left to fill the volume takes the database and the HLS preview with it, and
// an upload can fill it just as well.
func TestSaveRefusesWhenTheDiskIsFull(t *testing.T) {
	s := newStore(t)
	s.freeBytes = func(string) (uint64, error) { return 100, nil }
	if _, err := s.Save(strings.NewReader("data"), "x.mp4", 0, 1<<30); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("err = %v, want ErrNoSpace", err)
	}
	assertDirEmpty(t, s)
}

func TestDeleteRefusesEscapes(t *testing.T) {
	s := newStore(t)
	outside := filepath.Join(filepath.Dir(s.Dir()), "secret.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("../secret.txt"); err == nil {
		t.Fatal("Delete escaped the uploads directory")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("Delete removed a file outside the uploads dir: %v", err)
	}
}

// A half-written upload must not be offered as a source.
func TestListSkipsPartials(t *testing.T) {
	s := newStore(t)
	if err := os.WriteFile(filepath.Join(s.Dir(), ".partial-123"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("List = %+v, want no partials", list)
	}
}

// ------------------------------------------------------------------ helpers

func assertDirEmpty(t *testing.T, s *Store) {
	t.Helper()
	entries, err := os.ReadDir(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Fatalf("uploads dir should be empty, found %q", e.Name())
	}
}

type failingReader struct {
	n     int
	after int
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n >= f.after {
		return 0, errors.New("connection reset")
	}
	n := copy(p, "abcdefgh")
	f.n += n
	return n, nil
}

// Origin is what tells an operator whether the server captured a file or they
// supplied it. It must be present on every path that produces a File, because
// a mixed listing with a blank tag on some rows is worse than no tag at all.
func TestOriginIsAlwaysSetOnEveryPath(t *testing.T) {
	s := newStore(t)
	saved, err := s.Save(strings.NewReader("bytes"), "a.mp4", 0, 0)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.Origin != OriginUploaded {
		t.Fatalf("Save Origin = %q, want %q", saved.Origin, OriginUploaded)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List returned %d items", len(list))
	}
	if list[0].Origin != OriginUploaded {
		t.Fatalf("List Origin = %q, want %q", list[0].Origin, OriginUploaded)
	}
}

// The three origins must stay distinct: a UI filtering on them collapses two
// categories into one the moment any pair matches.
func TestOriginConstantsAreDistinct(t *testing.T) {
	all := []string{OriginRecorded, OriginUploaded, OriginClip}
	seen := map[string]bool{}
	for _, o := range all {
		if o == "" {
			t.Fatal("an origin constant is empty")
		}
		if seen[o] {
			t.Fatalf("duplicate origin value %q", o)
		}
		seen[o] = true
	}
}

// The floor must survive the upload, not merely precede it. Checking only
// "is there 2 GiB free" accepts an 8 GiB upload onto a volume with exactly the
// reserve free, then writes until ENOSPC and eats the reserve the database and
// recorder depend on -- the exact thing the floor exists to protect.
func TestSaveRequiresHeadroomForTheUploadItself(t *testing.T) {
	s := newStore(t)
	const twoGiB = uint64(2) << 30
	// Exactly the floor free, and an upload that would consume it.
	s.freeBytes = func(string) (uint64, error) { return twoGiB, nil }
	if _, err := s.Save(strings.NewReader("x"), "big.mp4", 8<<30, twoGiB); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("err = %v, want ErrNoSpace: the floor was not reserved for the upload", err)
	}
	assertDirEmpty(t, s)

	// Room for both floor and upload: accepted.
	s.freeBytes = func(string) (uint64, error) { return twoGiB + (16 << 30), nil }
	if _, err := s.Save(strings.NewReader("x"), "ok.mp4", 8<<30, twoGiB); err != nil {
		t.Fatalf("refused an upload that fits: %v", err)
	}
}

// A disk check that cannot run must FAIL CLOSED. The one case where you cannot
// tell how much room is left is not the case to start writing gigabytes.
func TestSaveFailsClosedWhenTheDiskCheckErrors(t *testing.T) {
	s := newStore(t)
	s.freeBytes = func(string) (uint64, error) { return 0, errors.New("statfs exploded") }
	if _, err := s.Save(strings.NewReader("data"), "x.mp4", 0, 1<<30); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("err = %v, want ErrNoSpace; an unreadable disk check must not skip the guard", err)
	}
	assertDirEmpty(t, s)
}
