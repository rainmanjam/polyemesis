package supervisor

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testSink(t *testing.T, maxBytes int64, maxFiles int) (*FileSink, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := newFileSink(discardLog(), dir, maxBytes, maxFiles)
	if err != nil {
		t.Fatalf("newFileSink: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func line(text string) LogLine {
	return LogLine{Time: time.Unix(0, 0).UTC(), Process: "ingest", Text: text, Level: "error"}
}

// sinkFiles names every file the sink left behind, so a test can assert the
// bound on file count rather than just on the live file's size.
func sinkFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestPersistedLineCarriesTimeLevelAndProcess(t *testing.T) {
	s, dir := testSink(t, 1<<20, 3)
	s.WriteLog(line("connection refused"))
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, logFileName))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	got := string(b)
	for _, want := range []string{"1970-01-01T00:00:00.000Z", "error", "ingest", "connection refused"} {
		if !strings.Contains(got, want) {
			t.Errorf("persisted line %q is missing %q", strings.TrimSpace(got), want)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Error("persisted line must be newline terminated")
	}
}

func TestRotationKeepsAtMostMaxFiles(t *testing.T) {
	const maxFiles = 3
	// Each formatted line is well over 40 bytes, so every write rotates and
	// the file count is the only thing holding the total down.
	s, dir := testSink(t, 40, maxFiles)
	for i := 0; i < 20; i++ {
		s.WriteLog(line("burst"))
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	names := sinkFiles(t, dir)
	if len(names) != maxFiles {
		t.Fatalf("kept %d files (%v), want %d", len(names), names, maxFiles)
	}
	for _, want := range []string{logFileName, logFileName + ".1", logFileName + ".2"} {
		if !slices.Contains(names, want) {
			t.Errorf("missing rotated file %q; got %v", want, names)
		}
	}
	if s.Dropped() != 0 {
		t.Errorf("dropped %d lines while rotating; the queue should have absorbed them", s.Dropped())
	}
}

func TestRotationPromotesNewestToTheLowestIndex(t *testing.T) {
	s, dir := testSink(t, 40, 3)
	for _, text := range []string{"oldest", "middle", "newest"} {
		s.WriteLog(line(text))
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	for _, tt := range []struct{ file, want string }{
		{logFileName, "newest"},
		{logFileName + ".1", "middle"},
		{logFileName + ".2", "oldest"},
	} {
		b, err := os.ReadFile(filepath.Join(dir, tt.file))
		if err != nil {
			t.Fatalf("read %s: %v", tt.file, err)
		}
		if !strings.Contains(string(b), tt.want) {
			t.Errorf("%s holds %q, want the line %q", tt.file, strings.TrimSpace(string(b)), tt.want)
		}
	}
}

func TestSingleFileCapTruncatesRatherThanArchiving(t *testing.T) {
	s, dir := testSink(t, 40, 1)
	s.WriteLog(line("first"))
	s.WriteLog(line("second"))
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if names := sinkFiles(t, dir); len(names) != 1 {
		t.Fatalf("maxFiles=1 left %v; want only the live file", names)
	}
	b, err := os.ReadFile(filepath.Join(dir, logFileName))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(b), "first") {
		t.Error("the rotated-away line must not survive when only one file is kept")
	}
}

func TestARestartAppendsRatherThanTruncating(t *testing.T) {
	dir := t.TempDir()
	first, err := newFileSink(discardLog(), dir, 1<<20, 3)
	if err != nil {
		t.Fatalf("newFileSink: %v", err)
	}
	first.WriteLog(line("before the crash"))
	first.Close()

	second, err := newFileSink(discardLog(), dir, 1<<20, 3)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	second.WriteLog(line("after the restart"))
	second.Close()

	b, err := os.ReadFile(filepath.Join(dir, logFileName))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	for _, want := range []string{"before the crash", "after the restart"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("log lost %q across the restart", want)
		}
	}
}

// A full queue must cost log lines, never latency: the caller is the goroutine
// draining the child's stderr pipe, and blocking it blocks the child.
func TestAFullQueueDropsLinesInsteadOfBlocking(t *testing.T) {
	s := &FileSink{
		log:  discardLog(),
		ch:   make(chan LogLine, 2),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10; i++ {
			s.WriteLog(line("flood"))
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WriteLog blocked with no writer draining the queue")
	}
	if got := s.Dropped(); got != 8 {
		t.Errorf("dropped %d of 10 lines into a 2-deep queue, want 8", got)
	}
}

func TestWritesAfterCloseAreDiscarded(t *testing.T) {
	s, dir := testSink(t, 1<<20, 3)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s.WriteLog(line("too late"))

	b, err := os.ReadFile(filepath.Join(dir, logFileName))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(b), "too late") {
		t.Error("a closed sink must not write")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	s, _ := testSink(t, 1<<20, 3)
	if err := s.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestNewFileSinkRejectsUnboundedConfigurations(t *testing.T) {
	tests := []struct {
		name      string
		maxFileMB int
		maxFiles  int
	}{
		{"zero size cap would never rotate", 0, 3},
		{"negative size cap", -1, 3},
		{"zero file count keeps nothing", 8, 0},
		{"negative file count", 8, -2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewFileSink(discardLog(), t.TempDir(), tt.maxFileMB, tt.maxFiles); err == nil {
				t.Fatalf("NewFileSink(%dMB, %d files) accepted an unbounded configuration",
					tt.maxFileMB, tt.maxFiles)
			}
		})
	}
}

func TestCapturedLinesReachTheConfiguredSink(t *testing.T) {
	s, dir := testSink(t, 1<<20, 3)
	p := New(discardLog(), Spec{Name: "dest:3", LogSink: s})
	p.appendLog("Connection to tcp://x failed", "error")
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, logFileName))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(b), "dest:3") || !strings.Contains(string(b), "Connection to tcp://x failed") {
		t.Errorf("sink did not receive the captured line; got %q", strings.TrimSpace(string(b)))
	}
	// The ring is the live view and must keep working alongside persistence.
	if got := p.Logs(); len(got) != 1 {
		t.Errorf("in-memory ring holds %d lines, want 1", len(got))
	}
}
