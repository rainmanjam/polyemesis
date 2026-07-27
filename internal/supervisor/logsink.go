package supervisor

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// LogWriter persists captured log lines. Implementations are called from the
// stderr reader goroutine and must never block it: a supervisor that stalls
// waiting on a disk write stops draining the child's stderr pipe, and a full
// pipe stops the child.
type LogWriter interface {
	WriteLog(LogLine)
}

const (
	// sinkQueue absorbs the burst FFmpeg emits when a connection collapses
	// while the writer is mid-rotation. Bounded so a wedged disk costs a
	// known amount of memory rather than growing without limit.
	sinkQueue    = 4096
	logFileName  = "process.log"
	logFilePerm  = 0o640
	logDirPerm   = 0o750
	sinkTimeFmt  = "2006-01-02T15:04:05.000Z07:00"
	bytesPerMiB  = 1024 * 1024
	errLogEveryN = 1000
)

// FileSink appends log lines to a rotating file set under the data directory.
// Total bytes on disk are bounded by maxFiles*maxBytes, which is the whole
// point: persistence that can fill the volume is worse than no persistence.
type FileSink struct {
	log      *slog.Logger
	path     string
	maxBytes int64
	maxFiles int

	ch   chan LogLine
	stop chan struct{}
	done chan struct{}

	closeOnce sync.Once
	dropped   atomic.Uint64

	// Owned by the writer goroutine alone, so they need no lock.
	f          *os.File
	size       int64
	writeErrs  uint64
	reportedIO bool
}

// NewFileSink opens (or resumes appending to) the rotating log in dir.
func NewFileSink(log *slog.Logger, dir string, maxFileMB, maxFiles int) (*FileSink, error) {
	if maxFileMB < 1 {
		return nil, fmt.Errorf("log file size must be at least 1MB, got %d", maxFileMB)
	}
	if maxFiles < 1 {
		return nil, fmt.Errorf("log file count must be at least 1, got %d", maxFiles)
	}
	return newFileSink(log, dir, int64(maxFileMB)*bytesPerMiB, maxFiles)
}

// newFileSink takes the cap in bytes so tests can rotate without writing
// megabytes.
func newFileSink(log *slog.Logger, dir string, maxBytes int64, maxFiles int) (*FileSink, error) {
	if err := os.MkdirAll(dir, logDirPerm); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	s := &FileSink{
		log:      log.With("component", "logsink"),
		path:     filepath.Join(dir, logFileName),
		maxBytes: maxBytes,
		maxFiles: maxFiles,
		ch:       make(chan LogLine, sinkQueue),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	if err := s.open(); err != nil {
		return nil, err
	}
	go s.run()
	return s, nil
}

// Path is the live log file, for the UI to name in a "logs are here" hint.
func (s *FileSink) Path() string { return s.path }

// Dropped counts lines discarded because the writer could not keep up. A
// non-zero value means the persisted log has holes; the in-memory ring does
// not, so the two together still tell the whole story.
func (s *FileSink) Dropped() uint64 { return s.dropped.Load() }

// WriteLog queues a line. It never blocks and never fails: dropping a log line
// is always preferable to stalling the process that produced it.
func (s *FileSink) WriteLog(l LogLine) {
	select {
	case <-s.stop:
		return
	default:
	}
	select {
	case s.ch <- l:
	default:
		s.dropped.Add(1)
	}
}

// Close drains what is already queued and closes the file.
func (s *FileSink) Close() error {
	s.closeOnce.Do(func() { close(s.stop) })
	<-s.done
	return nil
}

func (s *FileSink) run() {
	defer close(s.done)
	for {
		select {
		case l := <-s.ch:
			s.write(l)
		case <-s.stop:
			// Drain rather than discard: the lines still queued at shutdown
			// are the ones written just before it, which is when a crash-time
			// log matters most.
			for {
				select {
				case l := <-s.ch:
					s.write(l)
				default:
					if s.f != nil {
						s.f.Close()
						s.f = nil
					}
					return
				}
			}
		}
	}
}

func (s *FileSink) write(l LogLine) {
	// A failed rotation leaves no open file. Retry here rather than giving up
	// for the rest of the session: the usual cause is a transient full volume,
	// and persistence should come back when it does.
	if s.f == nil {
		if err := s.open(); err != nil {
			s.reportIO("reopen log file", err)
			return
		}
	}
	line := fmt.Sprintf("%s %-7s %s: %s\n",
		l.Time.Format(sinkTimeFmt), l.Level, l.Process, l.Text)

	// Rotate before the write rather than after, so a single line can never
	// push a file past its cap.
	if s.size > 0 && s.size+int64(len(line)) > s.maxBytes {
		if err := s.rotate(); err != nil {
			s.reportIO("rotate log file", err)
			return
		}
	}

	n, err := s.f.WriteString(line)
	s.size += int64(n)
	if err != nil {
		s.reportIO("write log file", err)
		return
	}
	s.reportedIO = false
}

// reportIO logs the first failure and then only every errLogEveryN, because a
// full volume produces one failure per line and the cure must not be noisier
// than the disease.
func (s *FileSink) reportIO(what string, err error) {
	s.writeErrs++
	if !s.reportedIO || s.writeErrs%errLogEveryN == 0 {
		s.log.Error(what, "path", s.path, "err", err, "failures", s.writeErrs)
		s.reportedIO = true
	}
}

func (s *FileSink) open() error {
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, logFilePerm)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	s.f, s.size = f, info.Size()
	return nil
}

// rotate shifts process.log to process.log.1, .1 to .2 and so on, dropping
// whatever falls off the end.
func (s *FileSink) rotate() error {
	if s.f != nil {
		s.f.Close()
		s.f = nil
	}
	if s.maxFiles == 1 {
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return s.open()
	}

	oldest := fmt.Sprintf("%s.%d", s.path, s.maxFiles-1)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Highest index first: each rename lands on a slot just vacated.
	for i := s.maxFiles - 2; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", s.path, i)
		to := fmt.Sprintf("%s.%d", s.path, i+1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(s.path, s.path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return s.open()
}
