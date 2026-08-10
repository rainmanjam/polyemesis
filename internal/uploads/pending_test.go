package uploads

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Stage is the whole point of the split: bytes on disk that NOTHING can see.
//
// The caller inspects them and then decides. If a staged file were listable, or
// nameable by a playlist item, the inspection would be happening after the
// decision it is meant to inform -- which is precisely the ordering this
// replaced.
func TestStagedBytesAreOnDiskAndInvisible(t *testing.T) {
	s := newStore(t)
	p, err := s.Stage(strings.NewReader("some media bytes"), "My Show.mp4", 0, 0)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}

	if got, err := os.ReadFile(p.Path()); err != nil {
		t.Fatalf("the staged bytes are not readable: %v", err)
	} else if string(got) != "some media bytes" {
		t.Errorf("staged bytes = %q", got)
	}
	if p.Bytes() != int64(len("some media bytes")) {
		t.Errorf("Bytes() = %d", p.Bytes())
	}

	base := filepath.Base(p.Path())
	if !strings.HasPrefix(base, partialPrefix) {
		t.Errorf("the staged file is called %q, which does not carry the prefix List skips", base)
	}
	// The extension survives onto the temp name, so the file the caller probes
	// and the file the caller publishes are identical in every respect ffmpeg
	// looks at.
	if filepath.Ext(base) != ".mp4" {
		t.Errorf("the staged name %q lost the final name's extension", base)
	}
	if Listable(base) {
		t.Errorf("Listable(%q) = true; a staged file must never be offered", base)
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("a staged upload is listed: %+v", list)
	}
}

func TestCommitPublishesAndDiscardAfterwardsIsANoOp(t *testing.T) {
	s := newStore(t)
	p, err := s.Stage(strings.NewReader("bytes"), "show.mkv", 0, 0)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	staged := p.Path()

	f, err := p.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if f.Name != p.Name() {
		t.Errorf("Commit named the file %q, Stage promised %q", f.Name, p.Name())
	}
	if f.Origin != OriginUploaded || f.PullURL != PullURL(f.Name) {
		t.Errorf("Commit returned %+v", f)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Errorf("the staged name still exists after Commit: %v", err)
	}
	full, err := s.Resolve(f.Name)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	st, err := os.Stat(full)
	if err != nil {
		t.Fatalf("the committed file is missing: %v", err)
	}
	// Windows has no Unix permission bits: os.Chmod there can only toggle the
	// read-only attribute, so a committed file reports 0666 and the assertion
	// would be about the platform rather than about Commit. The 0600 is a real
	// property everywhere it means anything, which is where it is checked.
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}

	// This is what makes `defer p.Discard()` safe to write beside a Commit, and
	// the handler does exactly that.
	if err := p.Discard(); err != nil {
		t.Fatalf("Discard after Commit: %v", err)
	}
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("Discard after Commit destroyed the published file: %v", err)
	}
	if list, err := s.List(); err != nil || len(list) != 1 {
		t.Fatalf("List = %+v, %v; want the one committed file", list, err)
	}
}

func TestDiscardRemovesTheBytesAndIsIdempotent(t *testing.T) {
	s := newStore(t)
	p, err := s.Stage(strings.NewReader("bytes"), "show.mkv", 0, 0)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := p.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, err := os.Stat(p.Path()); !os.IsNotExist(err) {
		t.Errorf("Discard left the staged bytes: %v", err)
	}
	if err := p.Discard(); err != nil {
		t.Errorf("a second Discard errored: %v", err)
	}
	assertDirEmpty(t, s)

	// And it cannot be published afterwards. A handler that answered 400 and
	// then committed anyway would put back exactly the file it refused.
	if _, err := p.Commit(); err == nil {
		t.Error("Commit succeeded after Discard")
	}
}

// A Commit that fails leaves NOTHING listed, including when it fails after the
// rename has already published the file.
//
// The rename is the moment the file becomes real, and chmod runs after it. A
// caller that gets an error from Commit is going to answer 500, so anything
// left behind is a file in the Library that nobody was ever told about -- the
// same class as the publish-before-probe window, reached by a different route.
//
// The failure is induced by making the final path un-chmod-able, which is done
// by turning it into a directory: the rename then fails outright. That covers
// the earlier of the two failure points; the chmod branch is the one the Remove
// was added for and it is unreachable without root or an exotic filesystem, so
// what is pinned here is the invariant both branches must keep.
func TestAFailedCommitLeavesNothingListed(t *testing.T) {
	s := newStore(t)
	p, err := s.Stage(strings.NewReader("bytes"), "show.mkv", 0, 0)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	final, err := s.Resolve(p.Name())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// A non-empty directory cannot be replaced by a rename.
	if err := os.MkdirAll(filepath.Join(final, "blocker"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if _, err := p.Commit(); err == nil {
		t.Fatal("Commit onto a non-empty directory succeeded")
	}
	// The Pending still owns the bytes, so the caller's deferred Discard clears
	// them. A Commit that failed must not have consumed the upload.
	if err := p.Discard(); err != nil {
		t.Fatalf("Discard after a failed Commit: %v", err)
	}
	if _, err := os.Stat(p.Path()); !os.IsNotExist(err) {
		t.Errorf("the staged bytes survived: %v", err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, f := range list {
		t.Errorf("a failed Commit left %q listed", f.Name)
	}
}

// Listable is the one rule two packages ask, so it is pinned directly rather
// than only through List.
func TestListableSkipsStagedFilesAndSidecars(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"show-1a2b3c4d.mkv", true},
		{"weird.name.with.dots.ts", true},
		{".partial-123456", false},
		{".partial-123456.mkv", false},
		{".probe-show-1a2b3c4d.mkv.json", false},
		{"", false},
		// Not a prefix of either reserved word, so it is an ordinary upload.
		{".partialish.mkv", true},
	} {
		if got := Listable(tc.name); got != tc.want {
			t.Errorf("Listable(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
