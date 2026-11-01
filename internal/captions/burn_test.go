package captions

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// fakeAfter captures the clear timer so a test fires it by hand.
type fakeAfter struct {
	mu  sync.Mutex
	fns []func()
	ds  []time.Duration
}

func (f *fakeAfter) after(d time.Duration, fn func()) func() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := len(f.fns)
	f.fns, f.ds = append(f.fns, fn), append(f.ds, d)
	return func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.fns[i] = nil
		return true
	}
}

func (f *fakeAfter) fire(i int) {
	f.mu.Lock()
	fn := f.fns[i]
	f.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func readBurn(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read burn file: %v", err)
	}
	return string(b)
}

// TestTheBurnFileShowsEachCaptionForItsLengthAndThenGoesBlank is the writer
// behind burned-in captions. A rendition's drawtext re-reads the file on every
// frame, so the file is the caption on screen: it holds the newest caption,
// wrapped to two lines, for as long as the words took (at least burnMinHold),
// and is then emptied. It is never removed, because drawtext refuses to start,
// and stops, on a missing file (measured against FFmpeg 8.1.3).
//
// MUTATION: the clear timer never armed. Observed: FAIL, the caption stayed on
// screen after its time.
func TestTheBurnFileShowsEachCaptionForItsLengthAndThenGoesBlank(t *testing.T) {
	path := filepath.Join(t.TempDir(), "burn.txt")
	if err := EnsureBurnFile(path); err != nil {
		t.Fatalf("EnsureBurnFile: %v", err)
	}
	if got := readBurn(t, path); got != "" {
		t.Fatalf("a new burn file holds %q, want empty", got)
	}
	fa := &fakeAfter{}
	w := NewBurnWriter(path, fa.after)
	long := strings.Repeat("caption words ", 10)
	if err := w.Show(transcribe.LiveCaption{Segment: transcribe.Segment{StartMS: 0, EndMS: 5000, Text: long}}); err != nil {
		t.Fatalf("Show: %v", err)
	}
	got := readBurn(t, path)
	lines := strings.Split(got, "\n")
	if len(lines) > 2 {
		t.Fatalf("burned caption has %d lines, want at most 2:\n%s", len(lines), got)
	}
	for _, l := range lines {
		if n := len([]rune(l)); n > burnLineRunes {
			t.Fatalf("burned line is %d characters, want at most %d: %q", n, burnLineRunes, l)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "caption words") {
		t.Fatalf("the newest words are not the ones on screen:\n%s", got)
	}
	if len(fa.ds) != 1 || fa.ds[0] != 5*time.Second {
		t.Fatalf("clear armed for %v, want once at the caption's 5 s", fa.ds)
	}
	fa.fire(0)
	if got := readBurn(t, path); got != "" {
		t.Fatalf("after its time the burn file holds %q, want empty", got)
	}

	// A short caption is held for the minimum, and a newer one cancels the
	// older one's clear so it cannot blank the newer caption early.
	_ = w.Show(transcribe.LiveCaption{Segment: transcribe.Segment{EndMS: 300, Text: "short"}})
	if fa.ds[1] != burnMinHold {
		t.Fatalf("short caption held %v, want the minimum %v", fa.ds[1], burnMinHold)
	}
	_ = w.Show(transcribe.LiveCaption{Segment: transcribe.Segment{EndMS: 4000, Text: "newer"}})
	fa.fire(1)
	if got := readBurn(t, path); got != "newer" {
		t.Fatalf("the older caption's clear blanked the newer one: %q", got)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := readBurn(t, path); got != "" {
		t.Fatalf("after Close the burn file holds %q, want empty (it must exist and be blank)", got)
	}
}

// TestNoBurnFileIsLeftHalfWrittenOrTakenAsATemplate checks that a caption is
// written whole, by rename, so drawtext never reads half of one, and that a
// caption's control characters are dropped rather than written into a file a
// filter reads.
func TestNoBurnFileIsLeftHalfWrittenOrTakenAsATemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "burn.txt")
	w := NewBurnWriter(path, (&fakeAfter{}).after)
	_ = w.Show(transcribe.LiveCaption{Segment: transcribe.Segment{EndMS: 1000, Text: "100% live\x00\x1b[31m"}})
	got := readBurn(t, path)
	if got != "100% live[31m" {
		t.Fatalf("burned text = %q, want the words with the control characters dropped", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("burn directory holds %d files, want only the burn file (no temp left behind)", len(entries))
	}
}
