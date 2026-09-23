//go:build !windows

package recording

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
)

// fakeFFprobe is an ffprobe that fails the way a truncated Matroska file makes
// the real one fail -- no duration -- and counts its own invocations, one line
// per call, in the returned file.
func fakeFFprobe(t *testing.T) (bin, calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	bin = filepath.Join(dir, "ffprobe")
	script := "#!/bin/sh\necho \"$@\" >> '" + calls + "'\n" +
		"echo '{\"streams\":[],\"format\":{}}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, calls
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

// Row 32. A segment the recorder never finalised -- a crash, a kill -9, a
// SIGKILL at the end of the grace -- has no duration for ffprobe to report, and
// never will: nothing remuxes it. The scan re-probed it every 30s for ever,
// logging the same WARN each time, because the only thing it remembered about
// a file was "has a duration". Probing a file whose bytes have not changed
// cannot give a different answer, so it is asked once per size.
func TestAnUnprobeableSegmentIsProbedOnceNotEveryScan(t *testing.T) {
	bin, calls := fakeFFprobe(t)
	var logs bytes.Buffer
	dir := t.TempDir()
	m := New(slog.New(slog.NewTextHandler(&logs, nil)), dbtest.Open(t), dir, nil, WithFFprobe(bin))

	now := time.Now()
	names := writeSegments(t, dir, now, []time.Duration{2 * time.Hour, time.Hour}, []int{524288, 1000})
	truncated := names[0] // names[1] is the newest, which is never probed

	for i := 0; i < 3; i++ {
		if _, err := m.Scan(); err != nil {
			t.Fatalf("scan %d: %v", i+1, err)
		}
	}
	if n := countLines(t, calls); n != 1 {
		t.Errorf("ffprobe ran %d times over three scans of an unchanged file, want 1", n)
	}
	if n := strings.Count(logs.String(), "file="+truncated); n != 1 {
		t.Errorf("%s was logged %d times over three scans, want once:\n%s", truncated, n, logs.String())
	}

	// A file that has CHANGED is a different question. Whatever rewrote it --
	// an operator's remux, a late flush -- may have given it a duration.
	f, err := os.OpenFile(filepath.Join(dir, truncated), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("more"))
	_ = f.Close()
	if _, err := m.Scan(); err != nil {
		t.Fatal(err)
	}
	if n := countLines(t, calls); n != 2 {
		t.Errorf("ffprobe ran %d times after the file changed, want it asked again (2)", n)
	}
}
