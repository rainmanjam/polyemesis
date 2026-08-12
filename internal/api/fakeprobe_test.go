package api

// THE STAND-IN FOR ffprobe, AND WHY IT IS NOT A SHELL SCRIPT.
//
// probeUpload reaches ffprobe by spawning a PROCESS, and the behaviours the
// probe suite pins -- a probe still running when the client goes away, a probe
// still running when somebody else calls GET /api/v1/media, a probe that
// outlives its timeout, sixteen probes competing for a bound -- only exist
// because that process takes real time. A function seam would delete the very
// thing under test, so the stand-in has to be a real program.
//
// It used to be a `#!/bin/sh` file, which Windows will not execute, so every
// test that needed a probe with a controllable lifetime skipped there: the
// client-disconnect survival, the probe timeout, the staged-not-listable
// window, the two WARN lines and the concurrency bound were unverified on the
// one platform whose process semantics differ most (#190, #265).
//
// It is now THE TEST BINARY RE-EXECUTING ITSELF, which is what
// internal/supervisor/testfake_test.go does and for the same reason. The
// difference from a helper compiled by `go build` inside the test is cost, and
// on this package the cost is not academic: `go build, vet, test` measures
// 7.06% flake and `test: windows-latest` 10.59% (#94, n=85), and #265 asks for
// the helper to be built once per package precisely so that job does not get
// slower. Re-execution builds it ZERO times per package -- the binary is
// already there, already compiled for the host, and needs no `go` on PATH at
// test time. Nothing about the fake needs a separate program; it needs a
// program with a lifetime the test controls, and the test binary is one.
//
// NO PLATFORM BRANCH ANYWHERE BELOW. Every primitive it uses -- os.WriteFile,
// os.Stat, time.Sleep, os.Mkdir as a lock -- is portable, and the shell
// constructs that were not (`exec`, `touch`, `until mkdir`, `sed -e '$d'`) have
// portable equivalents rather than Windows counterparts.
//
// THE PLAN ARRIVES BY ENVIRONMENT, not by argument, because the arguments are
// not ours: ffmpeg.probeFile builds ffprobe's own argv and the test only gets
// to choose the path. t.Setenv is the right instrument for that -- it restores
// the variable when the test ends, and it panics if the test is parallel, which
// is the exact condition under which one process-wide plan would be wrong.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeProbeEnv carries the JSON plan to a re-executed test binary. Its presence
// is what tells TestMain it is a fake probe rather than a test run, so it is
// read before any test flag is parsed and must not be set by anything else.
const fakeProbeEnv = "POLYEMESIS_FAKE_PROBE_PLAN"

// fakeProbePlan is everything a stand-in ffprobe can be asked to do. Each field
// replaces one line of the shell script it came from; the zero value is a probe
// that touches Started and exits 0 saying nothing.
type fakeProbePlan struct {
	// Started is touched on entry, so a test can wait for the probe to be in
	// flight instead of sleeping and hoping. Set by fakeProbe, not by callers.
	Started string
	// Release, when set, blocks the probe until that file appears -- the probe
	// is held inside the handler for exactly as long as the assertions need.
	Release string
	// Sleep, when set, keeps the probe alive that long. It is a plain
	// time.Sleep in a single process, which is what the old script's `exec`
	// bought by hand: with a shell wrapper the sleeper survived the kill still
	// holding ffprobe's stdout pipe, and the handler waited out cmd.WaitDelay.
	// There is no wrapper to survive here, so there is nothing to exec.
	Sleep time.Duration
	// Stdout is printed to stdout, where ffprobe's JSON would go.
	Stdout string
	// Stderr is printed to stderr, where ffprobe's diagnostics would go.
	Stderr string
	// Exit is the exit code.
	Exit int
	// Live and Peak are the concurrency census: one line is appended to Live
	// while this probe is alive and removed when it leaves, and Peak keeps the
	// high-water mark. Both are guarded by os.Mkdir on Live+".lock", which is
	// an atomic create-or-fail on every platform Go runs on.
	Live string
	Peak string
	// Hold is how long a counted probe stays alive once it is on the Live list.
	Hold time.Duration
}

// fakeProbe points the server at a stand-in ffprobe running the given plan and
// returns its path.
//
// The path is this test binary. The plan reaches it through the environment,
// scoped to the calling test by t.Setenv.
func fakeProbe(t *testing.T, started string, plan fakeProbePlan) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("cannot locate the test binary to re-execute as a fake ffprobe: %v", err)
	}
	plan.Started = started
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("encode fake probe plan: %v", err)
	}
	t.Setenv(fakeProbeEnv, string(encoded))
	return self
}

// THE FAKE PROBE IS A PROBE, NOT A TEST SUITE, and this is the one assertion
// nothing else in the package can make.
//
// TestMain's dispatch is what keeps a re-executed test binary from reaching
// m.Run. Lose it and the child runs the whole package -- printing a testing
// summary onto the pipe ffmpeg.probeFile is parsing as ffprobe's JSON, and
// spawning its own probe children while it does. Every OTHER test that uses
// fakeProbe would then fail for a reason that looks like a product bug, and one
// of them would look like a hang. This one names the actual cause.
//
// It drives the child directly, with the plan on cmd.Env rather than t.Setenv,
// because the claim is about what THIS binary does when it is handed a plan --
// not about what the upload handler does with the result.
func TestTheFakeProbeRunsItsPlanRatherThanThePackagesTests(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	started := filepath.Join(t.TempDir(), "started")
	plan, err := json.Marshal(fakeProbePlan{
		Started: started,
		Stdout:  `{"streams":[]}`,
		Stderr:  "a diagnostic",
		Exit:    3,
	})
	if err != nil {
		t.Fatalf("encode plan: %v", err)
	}

	// ffprobe's own argv, because that is what probeFile spawns it with and the
	// testing package would reject every one of these flags as unknown.
	cmd := exec.Command(self, "-hide_banner", "-v", "error", "-print_format", "json",
		"-show_streams", "somefile.mkv")
	cmd.Env = append(os.Environ(), fakeProbeEnv+"="+string(plan))
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		t.Fatalf("the fake probe did not exit with a status: %v (stderr %q)", runErr, stderr.String())
	}
	if got := exitErr.ExitCode(); got != 3 {
		t.Errorf("exit code = %d, want 3", got)
	}
	if _, err := os.Stat(started); err != nil {
		t.Errorf("the fake probe did not announce that it started: %v", err)
	}

	// EXACTLY the planned line. Not "contains": a testing summary on the same
	// pipe would still contain it, and that is the failure this test exists for.
	if got := strings.TrimRight(stdout.String(), "\r\n"); got != `{"streams":[]}` {
		t.Errorf("stdout = %q, want exactly the planned line. Anything else means the "+
			"re-executed binary reached m.Run, and ffmpeg.probeFile is parsing a "+
			"testing summary as ffprobe's JSON", got)
	}
	if got := strings.TrimRight(stderr.String(), "\r\n"); got != "a diagnostic" {
		t.Errorf("stderr = %q, want exactly the planned line", got)
	}
	// The clinching one: a test binary that ran its own suite says so.
	for _, tell := range []string{"--- PASS", "--- FAIL", "PASS\n", "FAIL\n", "no tests to run"} {
		if strings.Contains(stdout.String()+stderr.String(), tell) {
			t.Errorf("the re-executed binary ran the package's tests: found %q in its output", tell)
		}
	}
}

// runFakeProbe is the whole of a fake probe's life. It is called from TestMain
// before any test flag is parsed, and it must return rather than reaching
// m.Run: a test binary that ran its own suite here would print a testing
// summary onto the pipe ffmpeg.probeFile is parsing as ffprobe's JSON.
//
// Every failure of the fake itself exits 2 with a message that names the fake,
// so it cannot be mistaken for a verdict about the file being probed.
func runFakeProbe(encoded string) int {
	var plan fakeProbePlan
	if err := json.Unmarshal([]byte(encoded), &plan); err != nil {
		fmt.Fprintf(os.Stderr, "fake probe: cannot decode its plan: %v\n", err)
		return 2
	}
	if plan.Started != "" {
		if err := os.WriteFile(plan.Started, nil, 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "fake probe: cannot announce that it started: %v\n", err)
			return 2
		}
	}
	if plan.Live != "" {
		if err := fakeProbeEnterCensus(plan); err != nil {
			fmt.Fprintf(os.Stderr, "fake probe: %v\n", err)
			return 2
		}
		time.Sleep(plan.Hold)
		if err := fakeProbeLeaveCensus(plan); err != nil {
			fmt.Fprintf(os.Stderr, "fake probe: %v\n", err)
			return 2
		}
	}
	if plan.Release != "" {
		for {
			if _, err := os.Stat(plan.Release); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if plan.Sleep > 0 {
		time.Sleep(plan.Sleep)
	}
	if plan.Stdout != "" {
		fmt.Fprintln(os.Stdout, plan.Stdout)
	}
	if plan.Stderr != "" {
		fmt.Fprintln(os.Stderr, plan.Stderr)
	}
	return plan.Exit
}

// fakeProbeEnterCensus adds this probe to the live list and raises the peak.
func fakeProbeEnterCensus(plan fakeProbePlan) error {
	unlock, err := fakeProbeLock(plan.Live)
	if err != nil {
		return err
	}
	defer unlock()

	lines, err := fakeProbeLines(plan.Live)
	if err != nil {
		return err
	}
	lines = append(lines, "x")
	if err := fakeProbeWriteLines(plan.Live, lines); err != nil {
		return err
	}

	var peak int
	raw, err := os.ReadFile(plan.Peak)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read the peak file: %w", err)
	}
	if err == nil {
		if _, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &peak); err != nil {
			peak = 0
		}
	}
	if len(lines) > peak {
		if err := os.WriteFile(plan.Peak, []byte(fmt.Sprintf("%d\n", len(lines))), 0o600); err != nil {
			return fmt.Errorf("write the peak file: %w", err)
		}
	}
	return nil
}

// fakeProbeLeaveCensus removes this probe from the live list.
func fakeProbeLeaveCensus(plan fakeProbePlan) error {
	unlock, err := fakeProbeLock(plan.Live)
	if err != nil {
		return err
	}
	defer unlock()

	lines, err := fakeProbeLines(plan.Live)
	if err != nil {
		return err
	}
	if len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}
	return fakeProbeWriteLines(plan.Live, lines)
}

// fakeProbeLock takes a mutual exclusion held across processes, so two probes
// cannot interleave a read-modify-write of the live list.
//
// os.Mkdir rather than O_CREATE|O_EXCL on a file, and rather than any file
// locking API: mkdir is a single atomic create-or-fail syscall on both POSIX
// and Windows, needs no unlink-on-crash cleanup story beyond the one below, and
// is what the shell version used for the same reason.
func fakeProbeLock(live string) (func(), error) {
	lock := live + ".lock"
	deadline := time.Now().Add(60 * time.Second)
	for {
		err := os.Mkdir(lock, 0o700)
		if err == nil {
			return func() { _ = os.Remove(lock) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("take the census lock: %w", err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the census lock %s was still held after 60s", lock)
		}
		time.Sleep(time.Millisecond)
	}
}

func fakeProbeLines(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read the live list: %w", err)
	}
	trimmed := strings.TrimRight(string(raw), "\n")
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\n"), nil
}

func fakeProbeWriteLines(path string, lines []string) error {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("write the live list: %w", err)
	}
	return nil
}
