package supervisor

// Scaffolding shared by the process-lifecycle tests.
//
// The fakes used to be /bin/sh scripts, which made the whole suite Unix-only —
// leaving zero coverage of process lifecycle on the one platform whose
// lifecycle differs most. They are now the test binary re-executing itself, so
// the same fakes run everywhere without depending on sh, cmd.exe, or their
// wildly different quoting, exit-code and signalling rules.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeChildFlag marks a re-execution of the test binary as a fake child. It is
// read straight out of os.Args before the testing package parses flags.
const fakeChildFlag = "-polyemesis-fake-child"

func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == fakeChildFlag {
		os.Exit(runFakeChild(os.Args[2], os.Args[3:]))
	}
	os.Exit(m.Run())
}

// runFakeChild is the whole of a fake child's life. It must exit without ever
// reaching m.Run, or the testing package would print its own summary onto the
// pipes the supervisor is reading.
func runFakeChild(mode string, args []string) int {
	switch mode {
	case "exit":
		code, err := strconv.Atoi(args[0])
		if err != nil {
			return 2
		}
		return code

	case "sleep":
		d, err := time.ParseDuration(args[0])
		if err != nil {
			return 2
		}
		// No signal handlers: the default disposition is what a supervised
		// FFmpeg would have, and it is what the stop path must be tested
		// against.
		time.Sleep(d)
		return 0

	case "stderr":
		n, err := strconv.Atoi(args[0])
		if err != nil {
			return 2
		}
		for i := 0; i < n; i++ {
			fmt.Fprintf(os.Stderr, "line%d\n", i)
		}
		return 0

	default:
		fmt.Fprintf(os.Stderr, "fake child: unknown mode %q\n", mode)
		return 2
	}
}

// fake is a command line that spawns one of the fake children above.
type fake struct {
	bin  string
	args []string
}

func newFake(mode string, args ...string) fake {
	// os.Executable rather than os.Args[0]: the latter is not guaranteed to be
	// a path the child can be spawned from.
	self, err := os.Executable()
	if err != nil {
		panic("cannot locate the test binary to re-execute as a fake child: " + err.Error())
	}
	return fake{bin: self, args: append([]string{fakeChildFlag, mode}, args...)}
}

// fakeExit spawns a child that exits immediately with code.
func fakeExit(code int) fake { return newFake("exit", strconv.Itoa(code)) }

// fakeSleep spawns a child that stays up until it is signalled.
func fakeSleep(d time.Duration) fake { return newFake("sleep", d.String()) }

// fakeStderr spawns a child that writes n lines to stderr and exits cleanly.
func fakeStderr(n int) fake { return newFake("stderr", strconv.Itoa(n)) }

func testProcess(t *testing.T, f fake, spec Spec) *Process {
	t.Helper()
	spec.Bin, spec.Args = f.bin, f.args
	if spec.Name == "" {
		spec.Name = t.Name()
	}
	p := New(slog.New(slog.NewTextHandler(io.Discard, nil)), spec)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		p.Stop(ctx)
	})
	return p
}

// waitTimeout is generous because every fake child costs a process spawn, and
// spawning is an order of magnitude slower on Windows than on Unix. It is only
// ever paid in full by a genuinely failing test.
const waitTimeout = 15 * time.Second

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// recorder collects the state transitions the supervisor announces. Polling
// Status() cannot stand in for it: a process is born in StateStopped, so
// "wait until stopped" would pass before the child ever ran.
type recorder struct {
	mu     sync.Mutex
	states []State
	pids   map[int]bool
}

func newRecorder() *recorder { return &recorder{pids: map[int]bool{}} }

func (r *recorder) onState(st Status) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, st.State)
	if st.State == StateRunning {
		r.pids[st.PID] = true
	}
}

func (r *recorder) saw(s State) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, seen := range r.states {
		if seen == s {
			return true
		}
	}
	return false
}

func (r *recorder) distinctPIDs() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pids)
}
