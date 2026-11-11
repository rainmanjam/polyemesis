package captions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// BURNED-IN CAPTIONS (#8).
//
// A rendition that burns the live captions in runs drawtext with textfile and
// reload=1, which re-reads the file on every frame. FFmpeg's subtitles filter
// was measured first and cannot do this: it reads its whole file once, when the
// filter starts, so a caption written after the encode began is never drawn
// (FFmpeg 8.1.3, the shipped image). drawtext with reload=1 was measured to draw
// a file replaced mid-encode.
//
// So the file IS the caption on screen. BurnWriter keeps it holding the newest
// caption for as long as its words took and empty otherwise. The rendition sits
// before any destination's broadcast delay, so a burned caption is delayed with
// the picture it is drawn on and stays in step with it.

const (
	// burnLineRunes is the longest burned line. About what fits across a 16:9
	// picture at the burn-in's type size.
	burnLineRunes = 42
	// burnLines is how many lines are on screen: the newest words.
	burnLines = 2
	// burnMinHold is the shortest time a caption stays on screen, so a one-word
	// caption can still be read.
	burnMinHold = 2 * time.Second
)

// BurnPath is where a programme's burn file lives under the data directory.
// The name is built from the numeric id alone.
func BurnPath(dataDir string, sourceID int64) string {
	return filepath.Join(dataDir, "captions", fmt.Sprintf("burn-source-%d.txt", sourceID))
}

// EnsureBurnFile creates the burn file empty when it is missing. drawtext
// refuses to start on a missing textfile, so a rendition that burns captions
// in must find one even before the first caption, and an empty file draws
// nothing at all (measured: the frame is identical to one without drawtext).
func EnsureBurnFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// ClearBurnFile leaves the burn file existing and empty, whatever it held.
func ClearBurnFile(path string) error {
	return NewBurnWriter(path, nil).Close()
}

// BurnWriter keeps one burn file showing the caption on screen. It is safe for
// concurrent use and safe on a nil receiver.
type BurnWriter struct {
	mu    sync.Mutex
	path  string
	after AfterFunc
	stop  func() bool
	gen   uint64
}

// NewBurnWriter returns a writer for path. after nil uses time.AfterFunc.
func NewBurnWriter(path string, after AfterFunc) *BurnWriter {
	if after == nil {
		after = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	return &BurnWriter{path: path, after: after}
}

// Show puts c on screen and arms its clear, cancelling the previous caption's.
func (w *BurnWriter) Show(c transcribe.LiveCaption) error {
	if w == nil {
		return nil
	}
	hold := time.Duration(c.EndMS-c.StartMS) * time.Millisecond
	if hold < burnMinHold {
		hold = burnMinHold
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stop != nil {
		w.stop()
	}
	w.gen++
	gen := w.gen
	if err := w.write(burnText(c.Text)); err != nil {
		return err
	}
	w.stop = w.after(hold, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.gen == gen {
			_ = w.write("")
		}
	})
	return nil
}

// Close cancels the pending clear and leaves the file empty, never removed.
func (w *BurnWriter) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stop != nil {
		w.stop()
		w.stop = nil
	}
	w.gen++
	return w.write("")
}

// write replaces the file whole, through a rename in the same directory, so a
// frame never reads half a caption. Caller holds mu.
func (w *BurnWriter) write(text string) error {
	dir := filepath.Dir(w.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".burn-*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), w.path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// burnText is a caption as burned: control characters dropped, words wrapped
// to burnLineRunes, and only the newest burnLines lines kept.
func burnText(s string) string {
	lines := wrapBurnWords(stripBurnControls(s))
	if len(lines) > burnLines {
		lines = lines[len(lines)-burnLines:]
	}
	return strings.Join(lines, "\n")
}

// stripBurnControls turns newlines and tabs into spaces and drops every other
// control character.
func stripBurnControls(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			if r == '\n' || r == '\t' {
				return ' '
			}
			return -1
		}
		return r
	}, s)
}

// wrapBurnWords greedily packs the words of clean into lines of at most
// burnLineRunes runes.
func wrapBurnWords(clean string) []string {
	var lines []string
	cur := ""
	for _, word := range strings.Fields(clean) {
		lines, cur, word = splitOverlongWord(lines, cur, word)
		switch {
		case cur == "":
			cur = word
		case len([]rune(cur))+1+len([]rune(word)) <= burnLineRunes:
			cur += " " + word
		default:
			lines, cur = append(lines, cur), word
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// splitOverlongWord cuts a word longer than burnLineRunes into full-width lines,
// flushing the pending line first, and returns the remainder still to place.
func splitOverlongWord(lines []string, cur, word string) ([]string, string, string) {
	for len([]rune(word)) > burnLineRunes {
		rs := []rune(word)
		if cur != "" {
			lines, cur = append(lines, cur), ""
		}
		lines, word = append(lines, string(rs[:burnLineRunes])), string(rs[burnLineRunes:])
	}
	return lines, cur, word
}
