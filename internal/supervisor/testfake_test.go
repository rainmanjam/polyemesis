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
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

	case "deaf":
		// IGNORES SIGTERM, which is the case Stop's deadline exists for and the
		// only way to reach it without waiting out a real timeout. An FFmpeg
		// wedged on a stuck output socket behaves this way.
		//
		// signal.Notify INTO A CHANNEL NOBODY READS, not signal.Ignore, and the
		// difference is a Windows correctness bug rather than a style choice.
		// signal.Ignore takes the "ignore" path in runtime/sigqueue.go, which
		// CLEARS the wanted bit for the signal. With the bit clear, sigsend
		// refuses the delivery, and os_windows.go's ctrlHandler answers a
		// refused delivery by running the DEFAULT handler -- which terminates
		// the process. A fake called "deaf" that dies on the first signal is
		// the worst possible fixture: every test that trusts the name silently
		// asserts nothing. Notify keeps the bit set, the signal is queued to a
		// buffered channel with no reader, and the process genuinely ignores it
		// on both platforms.
		//
		// Both SIGTERM and os.Interrupt, because they are the same event on
		// different platforms: Unix stop sends SIGTERM, while the Windows path
		// arrives as a console control event that the runtime maps to SIGINT.
		//
		// (Windows behaviour here is read off the runtime sources, not executed
		// -- see the skip in TestStopReportsWhenItHadToKillTheChild.)
		deaf := make(chan os.Signal, 1)
		signal.Notify(deaf, syscall.SIGTERM, os.Interrupt)
		d, err := time.ParseDuration(args[0])
		if err != nil {
			return 2
		}
		// THE HANDSHAKE, and it must come after Notify. StateRunning is set the
		// instant cmd.Start() returns, which is before the child has executed
		// its first instruction; a test that signalled on the strength of that
		// could land the signal on a child whose handler was not installed yet,
		// and watch a "deaf" fake die obediently. Announcing readiness from
		// inside the child is the only report that cannot be early.
		fmt.Fprintln(os.Stderr, deafReadyLine)
		time.Sleep(d)
		return 0

	case "orphan":
		// A GRANDCHILD THAT OUTLIVES ITS PARENT AND INHERITS ITS PIPES. This is
		// the shape #194 was filed for, reduced to its mechanism: FFmpeg spawning
		// a helper that survives it does exactly this, and so does anything the
		// child forks and does not wait for.
		//
		// The grandchild inherits THESE fds -- os.Stdout and os.Stderr here are
		// the write ends of the supervisor's pipes -- so when this process exits,
		// the pipes do NOT reach EOF. A supervisor that drains to EOF before
		// reaping is then waiting on a process it never started, for as long as
		// the grandchild lives.
		d, err := time.ParseDuration(args[0])
		if err != nil {
			return 2
		}
		code, err := strconv.Atoi(args[1])
		if err != nil {
			return 2
		}
		self, err := os.Executable()
		if err != nil {
			return 2
		}
		gc := exec.Command(self, fakeChildFlag, "sleep", d.String())
		gc.Stdout, gc.Stderr = os.Stdout, os.Stderr
		if err := gc.Start(); err != nil {
			return 2
		}
		// Announced on stderr so the test can reach the grandchild to kill it:
		// it is re-parented to init the moment this returns, and a test that
		// leaves a 20-second sleeper behind on every run is its own problem.
		fmt.Fprintf(os.Stderr, "%s%d\n", orphanPIDPrefix, gc.Process.Pid)
		// A line the supervisor will classify as an error, so a test can assert
		// that the tail of stderr still becomes LastError -- the diagnostic the
		// bounded drain must not have traded away.
		fmt.Fprintln(os.Stderr, orphanLastWords)
		return code

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

// deafReadyLine is what a "deaf" child prints on stderr once its signal
// handlers are installed. It goes to stderr because the supervisor scans stderr
// into the log ring, where a test can see it; stdout is fed to the FFmpeg
// progress parser and would swallow it.
const deafReadyLine = "fake child: signal handlers installed"

// fakeDeaf spawns a child that ignores SIGTERM, so only SIGKILL ends it.
// Pair it with waitForDeaf: until the readiness line arrives the child is not
// yet deaf, and a signal delivered before then is answered by the default
// disposition.
func fakeDeaf(d time.Duration) fake { return newFake("deaf", d.String()) }

// waitForDeaf blocks until a fakeDeaf child has installed its handlers.
//
// VERIFIED DEAFNESS IS A PRECONDITION for anything the deadline arm asserts,
// and it is also the precondition on un-skipping that arm's test on Windows:
// without this, a Windows run cannot tell "the signal was ignored and the
// deadline won" from "the signal arrived a microsecond too early and the child
// died the ordinary way".
func waitForDeaf(t *testing.T, p *Process) {
	t.Helper()
	waitFor(t, "the deaf child to install its signal handlers", func() bool {
		for _, l := range p.Logs() {
			if strings.Contains(l.Text, deafReadyLine) {
				return true
			}
		}
		return false
	})
}

// fakeStderr spawns a child that writes n lines to stderr and exits cleanly.
func fakeStderr(n int) fake { return newFake("stderr", strconv.Itoa(n)) }

const (
	// orphanPIDPrefix introduces the pid of the grandchild an "orphan" child
	// leaves behind. Pair fakeOrphan with reapOrphan.
	orphanPIDPrefix = "fake child: orphan grandchild pid="
	// orphanLastWords is the last thing an "orphan" child writes before exiting.
	// It contains "error" so classify() files it as one and runOnce folds it into
	// LastError.
	orphanLastWords = "orphan child: error: the last words before exiting"
)

// fakeOrphan spawns a child that leaves a grandchild holding its stdout and
// stderr for grandchildLife, then exits with code.
func fakeOrphan(grandchildLife time.Duration, code int) fake {
	return newFake("orphan", grandchildLife.String(), strconv.Itoa(code))
}

// reapOrphan kills the grandchild a fakeOrphan child left behind, reading its
// pid out of the supervisor's own log ring.
//
// Registered by the test AFTER testProcess, so it runs BEFORE testProcess's Stop
// (t.Cleanup is LIFO) and the pid line is still in the ring either way.
func reapOrphan(t *testing.T, p *Process) {
	t.Helper()
	for _, l := range p.Logs() {
		_, rest, found := strings.Cut(l.Text, orphanPIDPrefix)
		if !found {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil {
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		// Kill AND Wait would be the hygienic pair, but this process is not our
		// child -- it was re-parented to init -- so there is nothing here to
		// reap. Kill is the whole of what this side can do.
		_ = proc.Kill()
		return
	}
	t.Errorf("no %q line in the log ring: the grandchild's pid was never announced, so this "+
		"test cannot prove it did not leave a %s sleeper behind", orphanPIDPrefix, "20s")
}

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
		_ = p.Stop(ctx)
	})
	return p
}

// waitTimeout is generous because every fake child costs a process spawn, and
// spawning is an order of magnitude slower on Windows than on Unix. It is only
// ever paid in full by a genuinely failing test.
const waitTimeout = 15 * time.Second

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	waitForBounded(t, waitTimeout, what, cond)
}

// waitForBounded is waitFor with the ceiling named at the call site.
//
// It exists because waitTimeout is deliberately generous, and a generous
// ceiling is the wrong instrument when the point of the poll is to distinguish
// a fast mechanism from a slow one that would satisfy it anyway. A caller that
// passes its own bound is asserting something about WHICH mechanism ran, and
// owes a comment saying why that number and not a larger one.
func waitForBounded(t *testing.T, bound time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(bound)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", bound, what)
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
