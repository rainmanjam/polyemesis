package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// A fixture with a destination child ACTUALLY RUNNING.
//
// It is here because its absence is the whole reason #150's argv leak survived
// four review rounds. Every disclosure guard in this package pointed Tools at
// /nonexistent/ffmpeg, so no child ever spawned, so the three egresses that
// render a live process were each excused from the sweep in writing:
//
//	"/api/v1/processes/{name}/logs": "needs a running child process"
//	"/api/v1/ws":                    "not an HTTP response body"
//
// and /api/v1/processes was IN the sweep but vacuous, because the fixture
// started only playout:source. Three excuses over one uncovered credential.
//
// This helper is the counterpart those excuses could never name. It is
// deliberately shared rather than local to one test: an excuse registry that
// demands a named counterpart is only honest if a counterpart is buildable.

var (
	faketoolOnce sync.Once
	faketoolBin  string
	faketoolErr  error
)

// faketoolPath compiles testdata/faketool and returns the binary.
//
// Compiled ONCE per test binary into a directory that outlives the individual
// test, and never looked up on PATH: the fixture must not depend on FFmpeg
// being installed, on its version, or on anything outside this module. A
// hermetic fixture is the difference between a guard that runs everywhere and
// one that is quietly skipped on CI.
func faketoolPath(t *testing.T) string {
	t.Helper()
	faketoolOnce.Do(func() {
		dir, err := os.MkdirTemp("", "faketool")
		if err != nil {
			faketoolErr = err
			return
		}
		bin := filepath.Join(dir, "faketool")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", bin, "./testdata/faketool")
		if out, err := cmd.CombinedOutput(); err != nil {
			faketoolErr = errWith(err, string(out))
			return
		}
		faketoolBin = bin
	})
	if faketoolErr != nil {
		t.Fatalf("build testdata/faketool: %v", faketoolErr)
	}
	return faketoolBin
}

type buildErr struct {
	err error
	out string
}

func (e buildErr) Error() string { return e.err.Error() + ": " + e.out }

func errWith(err error, out string) error { return buildErr{err: err, out: out} }

// liveTools points BOTH FFmpeg and FFprobe at the fake binary.
//
// Both, not just FFmpeg. The engine holds every destination down until it has
// measured the ingest layout, and the measurement is an ffprobe call: with a
// real FFmpeg and a missing ffprobe the destinations only start after five
// consecutive probe failures, which is fifteen seconds of nothing and a fixture
// that reports its own timeout as a pass.
func liveTools(t *testing.T) *ffmpeg.Tools {
	t.Helper()
	bin := faketoolPath(t)
	tools := defaultTools()
	tools.FFmpeg = bin
	tools.FFprobe = bin
	return tools
}

// The sentinels this fixture plants on the destination's COMMAND LINE. Distinct
// from the ones in read_scope_leak_test.go so a failure says which shape
// escaped, and each is the exact FFmpeg idiom that defeated alerts.Redact:
//
//	argvBackslashBearer  `-headers Authorization:Bearer\ X` -- SplitArgs makes
//	                     "Bearer\" and "X" separate argv entries, so the
//	                     bearerToken rule masks the word and returns the key.
//	argvRTMPConn         `-rtmp_conn S:X` -- the standard FFmpeg RTMP auth
//	                     idiom, matched by no rule in the table at all.
//	argvDestCredential   the destination's own stored StreamKey, reaching the argv
//	                     inside the publish URL. Named for what it IS rather than
//	                     for the column it comes from: `somethingKey = "<high
//	                     entropy>"` is exactly the shape gitleaks' generic-api-key
//	                     rule matches, and the right answer to a scanner finding a
//	                     fixture is to stop writing fixtures in a credential's
//	                     shape -- never to widen the allowlist that is what makes
//	                     the scanner worth reading at all.
const (
	argvBackslashBearer = "SENTINEL-argv-backslash-bearer-4c71"
	argvRTMPConn        = "SENTINEL-argv-rtmpconn-value-4c71"
	argvDestCredential  = "SENTINEL-argv-destination-cred-4c71"
	argvPassphrase      = "SENTINEL-argv-passphrase-flag-4c71"
)

func argvSentinels() []string {
	return []string{argvBackslashBearer, argvRTMPConn, argvDestCredential, argvPassphrase}
}

// runningDestServer builds a server whose destination "dest:1" is a live child
// with real stderr on the record.
//
// IngestPull rather than SRT, and that choice is load-bearing: an SRT source
// needs no ingest child at all -- srtserver delivers straight into the relay hub
// -- so nothing would ever put bytes on the hub, the probe loop would never run,
// the layout would never be measured, and the destinations would be held down
// for the life of the test. Pull spawns a child, the child pumps datagrams at
// the hub, and the rest follows.
func runningDestServer(t *testing.T) (http.Handler, *db.DB, func(*http.Request)) {
	t.Helper()
	h, store, sign := renditionServer(t, liveTools(t))

	src, err := store.GetSource(1)
	if err != nil {
		t.Fatalf("fixture source: %v", err)
	}
	src.Ingest.Mode = db.IngestPull
	src.Ingest.Pull.URL = "https://cam.example/live.m3u8"
	if err := store.UpdateSource(src); err != nil {
		t.Fatalf("fixture: choose pull ingest: %v", err)
	}

	dest, err := store.CreateDestination(&db.Destination{
		Name: "live-dest", Kind: db.DestRTMP,
		URL:       "rtmp://ingest.example/app",
		StreamKey: argvDestCredential,
		// The two shapes alerts.Redact provably cannot mask, spliced into the
		// argv exactly as an operator would type them into expert mode.
		ExtraInputArgs:  "-headers Authorization:Bearer\\ " + argvBackslashBearer,
		ExtraOutputArgs: "-rtmp_conn S:" + argvRTMPConn + " -passphrase " + argvPassphrase,
		AudioBitrate:    160, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}
	if dest.ID != 1 {
		t.Fatalf("fixture destination is %d, but the tests address dest:1", dest.ID)
	}

	s := serverUnderTest(t, h)
	if err := s.mgr.Reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	waitForRunningDest(t, h, sign)
	return h, store, sign
}

// waitForRunningDest blocks until dest:1 exists AND has emitted stderr.
//
// Both conditions, because they are different halves of the leak and one
// without the other is a fixture that proves less than it claims. The COMMAND
// half needs no child at all -- currentArgs falls back to spec.Args before the
// first spawn -- while the LOG half needs a process that actually ran and
// wrote. A helper that waited only for the process to appear would leave the
// /ws and "lines" assertions running against an empty ring, which is the
// vacuity this whole exercise exists to refuse.
func waitForRunningDest(t *testing.T, h http.Handler, sign func(*http.Request)) {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		lines := destLogLines(t, h, sign)
		if len(lines) > 0 {
			return
		}
		last = summariseProcesses(t, h, sign)
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("dest:1 never produced a log line within the budget; the fixture is not "+
		"live and every assertion built on it would be vacuous. processes: %s", last)
}

type procRow struct {
	Status struct {
		Name  string `json:"name"`
		Kind  string `json:"kind"`
		State string `json:"state"`
	} `json:"status"`
	Command string `json:"command"`
}

type procDetail struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Lines   []struct {
		Text string `json:"text"`
	} `json:"lines"`
}

func listProcesses(t *testing.T, h http.Handler, sign func(*http.Request)) []procRow {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/processes", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /processes: %d %s", w.Code, w.Body.String())
	}
	var rows []procRow
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode /processes: %v (%s)", err, w.Body.String())
	}
	return rows
}

func summariseProcesses(t *testing.T, h http.Handler, sign func(*http.Request)) string {
	t.Helper()
	var b strings.Builder
	for _, p := range listProcesses(t, h, sign) {
		b.WriteString(p.Status.Name + "=" + p.Status.State + " ")
	}
	return b.String()
}

func destLogLines(t *testing.T, h http.Handler, sign func(*http.Request)) []string {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/processes/dest:1/logs", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		return nil
	}
	var d procDetail
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		return nil
	}
	out := make([]string, 0, len(d.Lines))
	for _, l := range d.Lines {
		out = append(out, l.Text)
	}
	return out
}
