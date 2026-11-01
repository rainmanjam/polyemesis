package captions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// liveCue is a caption recognised at `at`, spoken over [at-lag-len, at-lag].
func liveCue(at time.Time, lag, length time.Duration, text string) transcribe.LiveCaption {
	return transcribe.LiveCaption{
		Segment: transcribe.Segment{StartMS: 0, EndMS: length.Milliseconds(), Text: text, Speaker: "Ana"},
		At:      at,
		LagMS:   lag.Milliseconds(),
	}
}

// TestTheCueLogRecordsWhenEachCaptionWasSpokenOnTheWallClock is what lines the
// live captions up with a recording and with the public player. The captioner's
// own StartMS is on its private clock, which starts when it does, so it matches
// neither a recording's timeline nor the player's. The log keeps the moment the
// words were spoken (ReleaseAt with no delay), and a reader asking for a window
// gets the cues that overlap it, oldest first.
//
// MUTATION: Append logs c.At (when the cue was recognised) as the start.
// Observed: FAIL, the start was 3.5 s late.
func TestTheCueLogRecordsWhenEachCaptionWasSpokenOnTheWallClock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cues.jsonl")
	log, err := OpenCueLog(path, 1<<20)
	if err != nil {
		t.Fatalf("OpenCueLog: %v", err)
	}
	base := time.UnixMilli(1_800_000_000_000)
	// Recognised 3.5 s after it began: 2 s of speech, 1.5 s of lag.
	if err := log.Append(liveCue(base.Add(3500*time.Millisecond), 1500*time.Millisecond, 2*time.Second, "hello")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := log.Append(liveCue(base.Add(60*time.Second), time.Second, time.Second, "much later")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, err := ReadCueLog(path, base, base.Add(10*time.Second))
	if err != nil {
		t.Fatalf("ReadCueLog: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("cues in the first ten seconds = %+v, want only the first", got)
	}
	if got[0].StartMS != base.UnixMilli() || got[0].EndMS != base.UnixMilli()+2000 {
		t.Fatalf("logged cue spans %d..%d, want %d..%d (when it was spoken)",
			got[0].StartMS, got[0].EndMS, base.UnixMilli(), base.UnixMilli()+2000)
	}
	if got[0].Text != "hello" || got[0].Speaker != "Ana" {
		t.Fatalf("logged cue = %+v, want hello by Ana", got[0])
	}
}

// TestTheCueLogStaysWithinItsByteBudgetAndKeepsTheNewestCues bounds the disk a
// long broadcast can take. Past the budget the file rolls over once: the full
// one becomes the .1 file (replacing an older one), so at most two budgets are
// on disk, and a reader still sees the newest cues across the roll.
//
// MUTATION: the roll-over check removed. Observed: FAIL, the log grew past
// twice its budget.
func TestTheCueLogStaysWithinItsByteBudgetAndKeepsTheNewestCues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cues.jsonl")
	const budget = 4096
	log, err := OpenCueLog(path, budget)
	if err != nil {
		t.Fatalf("OpenCueLog: %v", err)
	}
	base := time.UnixMilli(1_800_000_000_000)
	text := strings.Repeat("word ", 20)
	for i := 0; i < 400; i++ {
		if err := log.Append(liveCue(base.Add(time.Duration(i)*time.Second), 0, time.Second, text)); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	_ = log.Close()
	var total int64
	for _, p := range []string{path, path + ".1"} {
		if st, err := os.Stat(p); err == nil {
			total += st.Size()
		}
	}
	if total > 2*budget {
		t.Fatalf("cue log takes %d bytes on disk, want at most %d", total, 2*budget)
	}
	got, err := ReadCueLog(path, base, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("ReadCueLog: %v", err)
	}
	// The last cue was recognised at 399 s after one second of speech, so it
	// was spoken from 398 s.
	if len(got) == 0 || got[len(got)-1].StartMS != base.Add(398*time.Second).UnixMilli() {
		t.Fatalf("the newest cue is not in the log after the roll-over")
	}
	for i := 1; i < len(got); i++ {
		if got[i].StartMS < got[i-1].StartMS {
			t.Fatalf("cues out of order across the roll-over at %d", i)
		}
	}
}

// TestReadingACueLogSkipsALineItCannotParse treats the file as untrusted: a
// torn last line (a crash mid-write) or a hand-edited line is skipped, not an
// error that loses every other caption, and a missing log is no cues.
func TestReadingACueLogSkipsALineItCannotParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cues.jsonl")
	body := `{"start":1000,"end":2000,"text":"kept"}` + "\n" + `{"start":` + "\n" + strings.Repeat("x", 200000) + "\n" +
		`{"start":3000,"end":4000,"text":"also kept"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCueLog(path, time.UnixMilli(0), time.UnixMilli(10_000))
	if err != nil {
		t.Fatalf("ReadCueLog: %v", err)
	}
	if len(got) != 2 || got[0].Text != "kept" || got[1].Text != "also kept" {
		t.Fatalf("cues = %+v, want the two good lines", got)
	}
	none, err := ReadCueLog(filepath.Join(t.TempDir(), "absent.jsonl"), time.UnixMilli(0), time.UnixMilli(1))
	if err != nil || len(none) != 0 {
		t.Fatalf("a missing log = %v, %v; want no cues and no error", none, err)
	}
}
