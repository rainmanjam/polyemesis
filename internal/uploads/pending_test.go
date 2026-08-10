package uploads

import (
	"os"
	"path/filepath"
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
	if st.Mode().Perm() != 0o600 {
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
