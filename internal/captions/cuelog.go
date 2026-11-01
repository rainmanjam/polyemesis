package captions

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// THE LIVE CUE LOG (#8).
//
// The captions a viewer saw live, kept so they can be lined up with a recording
// afterwards and exported. Each line is one cue as JSON, with the wall-clock
// moment its words were spoken. The captioner's own StartMS is on a clock that
// starts with the captioner, which matches neither a recording's timeline nor a
// player's; the wall clock is what both can be mapped onto.
//
// The text is the text after the operator's policy (the emit boundary), so the
// log holds exactly what went out.

// DefaultCueLogBytes is the budget of one cue log file. A caption line is about
// 150 bytes, so this is several hundred thousand cues, far more than a day of
// speech. The log rolls over once past it, so at most twice this is on disk.
const DefaultCueLogBytes = 32 << 20

// maxCueLine bounds one line a reader accepts. A line longer than this is not
// one the writer produced, and is skipped.
const maxCueLine = 64 << 10

// LoggedCue is one cue in the log. Start and End are Unix milliseconds.
type LoggedCue struct {
	StartMS int64  `json:"start"`
	EndMS   int64  `json:"end"`
	Track   int    `json:"track,omitempty"`
	Speaker string `json:"speaker,omitempty"`
	Text    string `json:"text"`
}

// CueLogPath is where a programme's cue log lives under the data directory.
// The name is built from the numeric id alone.
func CueLogPath(dataDir string, sourceID int64) string {
	return filepath.Join(dataDir, "captions", fmt.Sprintf("cues-source-%d.jsonl", sourceID))
}

// CueLog appends cues to a programme's log. It is safe for concurrent use and
// safe on a nil receiver, which logs nothing.
type CueLog struct {
	mu   sync.Mutex
	f    *os.File
	path string
	size int64
	max  int64
	// closed is set by Close. A nil f with closed unset is a roll that failed,
	// and the next Append opens the file again rather than logging nothing.
	closed bool
}

// OpenCueLog opens the log for appending, creating it (and its directory) when
// it is missing. Captions are the broadcast's words, so the file is private to
// the server's user.
func OpenCueLog(path string, maxBytes int64) (*CueLog, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultCueLogBytes
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	l := &CueLog{path: path, max: maxBytes}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *CueLog) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, st.Size()
	return nil
}

// Append logs one cue at the wall-clock time it was spoken. Past the budget the
// file rolls over first: it becomes the .1 file, replacing an older one.
func (l *CueLog) Append(c transcribe.LiveCaption) error {
	if l == nil {
		return nil
	}
	start := ReleaseAt(c, 0)
	length := c.EndMS - c.StartMS
	if length < 0 {
		length = 0
	}
	line, err := json.Marshal(LoggedCue{
		StartMS: start.UnixMilli(),
		EndMS:   start.UnixMilli() + length,
		Track:   c.Track,
		Speaker: c.Speaker,
		Text:    c.Text,
	})
	if err != nil {
		return err
	}
	line = append(line, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	if l.f == nil {
		// An earlier roll could not reopen the file. Every Append tries again, so
		// one transient error does not silence the log for the rest of the show.
		if err := l.open(); err != nil {
			return err
		}
	}
	if l.size > 0 && l.size+int64(len(line)) > l.max {
		if err := l.roll(); err != nil {
			return err
		}
	}
	n, err := l.f.Write(line)
	l.size += int64(n)
	return err
}

// roll moves the full file aside and starts a new one. Caller holds mu.
func (l *CueLog) roll() error {
	if err := l.f.Close(); err != nil {
		return err
	}
	l.f = nil
	// A rename that fails leaves the full file where it is. The log goes on in it,
	// over budget, and the next Append tries the roll again; returning here
	// instead left the log closed and every later caption unlogged, silently.
	_ = os.Rename(l.path, l.path+".1")
	return l.open()
}

// Close closes the log. It is safe on a nil receiver and safe twice.
func (l *CueLog) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// ReadCueLog returns the logged cues that overlap [from, to], oldest first,
// reading the rolled-over file before the current one. A missing log is no
// cues. A line that does not parse, or is longer than any the writer produces,
// is skipped: the file is read as untrusted, and one torn line must not cost
// every other caption.
func ReadCueLog(path string, from, to time.Time) ([]LoggedCue, error) {
	lo, hi := from.UnixMilli(), to.UnixMilli()
	var out []LoggedCue
	for _, p := range []string{path + ".1", path} {
		cues, err := readCueFile(p, lo, hi)
		if err != nil {
			return nil, err
		}
		out = append(out, cues...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartMS < out[j].StartMS })
	return out, nil
}

func readCueFile(path string, lo, hi int64) ([]LoggedCue, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []LoggedCue
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := readBoundedLine(r)
		if len(line) > 0 {
			var c LoggedCue
			if json.Unmarshal(line, &c) == nil && c.EndMS >= lo && c.StartMS <= hi && c.EndMS >= c.StartMS {
				out = append(out, c)
			}
		}
		if err != nil {
			if errors.Is(err, errLineTooLong) {
				continue
			}
			break
		}
	}
	return out, nil
}

var errLineTooLong = errors.New("cue log line too long")

// readBoundedLine reads one line, without its newline. A line past maxCueLine
// is consumed to its end and reported as errLineTooLong with no bytes, so the
// reader holds at most maxCueLine of any line in memory.
func readBoundedLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	tooLong := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(chunk) > maxCueLine {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if tooLong {
			if err != nil {
				return nil, err
			}
			return nil, errLineTooLong
		}
		if n := len(line); n > 0 && line[n-1] == '\n' {
			line = line[:n-1]
		}
		return line, err
	}
}
