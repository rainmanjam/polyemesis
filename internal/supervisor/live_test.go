package supervisor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

// Live commands write to a child's stdin, which FFmpeg also reads its keys
// from. The tests here pin the three things that must hold for that to be safe:
// only well-formed commands are ever written, a reply is matched to the
// command that asked for it, and a missing reply can never be mistaken for a
// later one.

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// withReplyTimeout shortens the reply deadline for one test and restores it.
func withReplyTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := liveReplyTimeout
	liveReplyTimeout = d
	t.Cleanup(func() { liveReplyTimeout = prev })
}

// The writer's line grammar is the only thing that ever reaches FFmpeg's key
// handler. Anything outside it must be refused before a byte is written.
func TestVolumeLineIsTheOnlyShapeTheWriterEmits(t *testing.T) {
	good := []struct {
		name  string
		level float64
		want  string
	}{
		{"volume@trk0", 0.5, "volume@trk0 -1 volume 0.5\n"},
		{"volume@s2_trk1", 2, "volume@s2_trk1 -1 volume 2\n"},
		{"volume@trk3", 0, "volume@trk3 -1 volume 0\n"},
	}
	for _, tc := range good {
		got, err := volumeLine(tc.name, tc.level)
		if err != nil {
			t.Errorf("volumeLine(%q, %v): %v", tc.name, tc.level, err)
			continue
		}
		if got != tc.want {
			t.Errorf("volumeLine(%q, %v) = %q, want %q", tc.name, tc.level, got, tc.want)
		}
	}

	bad := []struct {
		name  string
		level float64
	}{
		{"trk0", 1},                  // not a volume instance
		{"volume@trk0\nq", 1},        // a newline would start a second command: `q` quits FFmpeg
		{"volume@a;b", 1},            // filter-graph punctuation
		{"volume@x -1 quit", 1},      // a space would smuggle an argument
		{"", 1},                      // empty
		{"volume@trk0", math.NaN()},  // not numeric
		{"volume@trk0", math.Inf(1)}, // not finite
		{"volume@trk0", -0.1},        // negative is not a gain
		{"volume@trk0", 101},         // beyond any gain the profile validator allows
	}
	for _, tc := range bad {
		if line, err := volumeLine(tc.name, tc.level); err == nil {
			t.Errorf("volumeLine(%q, %v) accepted %q; it must refuse before writing", tc.name, tc.level, line)
		}
	}
}

// A process that did not opt in has no pipe and must say so, not write to a
// nil or a wrong descriptor. The child is the re-executed test binary, so this
// runs on Windows too.
func TestSetVolumeRefusesAProcessThatDidNotOptIn(t *testing.T) {
	p := testProcess(t, fakeSleep(30*time.Second), Spec{Name: "plain"})
	p.Start()
	waitFor(t, "running", func() bool { return p.Status().State == StateRunning })

	if err := p.SetVolume("volume@trk0", 0.5); err != errLiveUnavailable {
		t.Fatalf("SetVolume on a non-opted-in process = %v, want errLiveUnavailable", err)
	}
}

// THE GUARD for "stdin is a kill switch": nothing but the command writer may
// name the stdin pipe. A second writer, a debug dump, or a helper that pokes
// the field would be a path for stray keys into FFmpeg. The allowed set is
// the writer (send) and the one place the pipe is created (runOnce).
func TestOnlyTheCommandWriterTouchesTheStdinPipe(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"send": true, "runOnce": true}
	var offenders []string
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if ok && sel.Sel.Name == "stdin" && !allowed[fn.Name.Name] {
						offenders = append(offenders, fset.Position(sel.Pos()).String()+" in "+fn.Name.Name)
					}
					// The pipe is created in one place. cmd.Stdin / cmd.StdinPipe
					// anywhere else would be a second way into FFmpeg's keys.
					if ok && (sel.Sel.Name == "Stdin" || sel.Sel.Name == "StdinPipe") && fn.Name.Name != "runOnce" {
						offenders = append(offenders, fset.Position(sel.Pos()).String()+" ("+sel.Sel.Name+") in "+fn.Name.Name)
					}
					return true
				})
			}
		}
	}
	if len(offenders) > 0 {
		t.Errorf("the stdin pipe is referenced outside the command writer:\n%s", strings.Join(offenders, "\n"))
	}
}

// A reply counts only when it is the whole stderr line FFmpeg prints. A line
// that merely contains the text (stream metadata, a title, an error echoing
// input) must not be read as an acknowledgement.
//
// The good lines are FFmpeg's own bytes, captured with cat -v from 6.1.6,
// 7.1.5, 8.1.3 and 9.0.2: every reply, acknowledgement or refusal, ends exactly
// at "res:". The last bad lines are the review's spoofs, which an earlier
// pattern (`(?: res:.*)?$`) accepted.
//
// MUTATION: restore `(?: res:.*)?$` in liveReplyRE. Observed: FAIL, "a non-reply
// line was taken as an acknowledgement" for the res: tails.
func TestOnlyAWholeReplyLineIsAnAcknowledgement(t *testing.T) {
	good := []string{
		"Command reply for stream 0: ret:0 res:",
		"Command reply for stream 0: ret:-22 res:",
		"Command reply for stream 0: ret:-38 res:",
		"Command reply for stream 1: ret:-78 res:",
	}
	for _, line := range good {
		if _, ret, ok := parseReply(line); !ok {
			t.Errorf("a genuine reply was not recognised: %q (ret %d)", line, ret)
		}
	}
	bad := []string{
		"title: Command reply for stream 0: ret:0 res:",
		"[warning] Command reply for stream 0: ret:0 res:",
		"Command reply for stream 0: ret:0 injected text",
		"x Command reply for stream 0: ret:0",
		"Command reply for stream 0: ret:0\nq",
		"Command reply for stream x: ret:0",
		// No FFmpeg prints a reply without " res:".
		"Command reply for stream 0: ret:0",
		// The review's spoofs: text after res: on the reply line itself.
		"Command reply for stream 0: ret:0 res:'",
		"Command reply for stream 0: ret:0 res:anything an attacker wants",
		"Command reply for stream 0: ret:0 res: ",
	}
	for _, line := range bad {
		if _, _, ok := parseReply(line); ok {
			t.Errorf("a non-reply line was taken as an acknowledgement: %q", line)
		}
	}
}

// Binding to the spawn: a reply that arrives for an earlier run must not reach
// the waiter of the current one, and an earlier run's teardown must not clear
// the current run's command channel.
func TestAReplyFromAnEarlierRunCannotAckTheNextCommand(t *testing.T) {
	p := New(quietLog(), Spec{Name: "live", Bin: "/bin/true", LiveCommands: true})
	old := &liveRun{replies: make(chan liveReply, 4)}
	cur := &liveRun{replies: make(chan liveReply, 4)}
	// Both mid-command, so it is the binding that is tested here and not the
	// in-flight gate (TestAReplyWithNoCommandInFlightIsDropped).
	old.inFlight.Store(true)
	cur.inFlight.Store(true)
	p.live = cur

	deliverReply(old, "Command reply for stream 0: ret:0 res:")
	select {
	case rep := <-cur.replies:
		t.Fatalf("a reply from the previous run (ret %d) acknowledged the current run's command", rep.ret)
	default:
	}

	p.clearLive(old)
	if p.live != cur {
		t.Fatal("tearing down an earlier run cleared the current run's command channel")
	}
	p.clearLive(cur)
	if p.live != nil {
		t.Fatal("the current run's own teardown did not clear its channel")
	}
}

// A reply line that arrives while no command is waiting is dropped by the
// scanner, so it cannot sit in the queue for the next command to read. The
// drain at the top of send would also discard it; this is the gate in front.
//
// MUTATION: remove the inFlight check from deliverReply. Observed: FAIL, "a
// reply with no command in flight was queued".
func TestAReplyWithNoCommandInFlightIsDropped(t *testing.T) {
	run := &liveRun{replies: make(chan liveReply, 4)}
	deliverReply(run, "Command reply for stream 0: ret:0 res:")
	if len(run.replies) != 0 {
		t.Fatal("a reply with no command in flight was queued")
	}
	run.inFlight.Store(true)
	deliverReply(run, "Command reply for stream 0: ret:0 res:")
	if len(run.replies) != 1 {
		t.Fatal("a reply during a command was not delivered")
	}
}

// FFmpeg reads the target with %63[^ ]. A longer name would be cut at 63 bytes
// and the rest read as the time, so it is refused before it is written.
func TestVolumeLineRefusesATargetFFmpegWouldTruncate(t *testing.T) {
	long := "volume@" + strings.Repeat("a", 57) // 64 bytes
	if line, err := volumeLine(long, 1); err == nil {
		t.Errorf("a 64-byte target was accepted: %q", line)
	}
	ok := "volume@" + strings.Repeat("a", 56) // 63 bytes
	if _, err := volumeLine(ok, 1); err != nil {
		t.Errorf("a 63-byte target was refused: %v", err)
	}
}

// A nil process (a destination with no child yet) refuses rather than panics,
// so the engine falls back to a restart.
func TestSetVolumeOnANilProcessIsRefused(t *testing.T) {
	var p *Process
	if err := p.SetVolume("volume@trk0", 1); err != errLiveUnavailable {
		t.Fatalf("nil SetVolume = %v, want errLiveUnavailable", err)
	}
}

// THE BINDING GUARD. A reply must reach the run whose scanner read it, never
// "whatever run the Process holds now". In the supervise loop a later spawn
// only starts after the earlier one's stderr is drained, so the crossing cannot
// be staged from outside; what CAN happen is a restart racing an unwinding
// predecessor (see the cmd compare-and-clear in runOnce), and that is the case
// this binding exists for. Since the race is not reproducible on demand, the
// rule is pinned structurally: the Process's `live` field is read only by
// SetVolume (to find the run to write to) and clearLive (compare-and-clear),
// and runOnce only ever assigns it. A scanner that looked the run up on the
// Process would have to read the field, and would fail this test.
//
// MUTATION: in runOnce, `deliverReply(liveCur, line)` ->
// `p.liveMu.Lock(); cur := p.live; p.liveMu.Unlock(); deliverReply(cur, line)`.
// Observed: FAIL, "p.live is read in runOnce".
func TestOnlySetVolumeAndClearLiveReadTheCurrentRun(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	readers := map[string]bool{"SetVolume": true, "clearLive": true}
	var offenders []string
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				// Selectors that are the left-hand side of an assignment are writes.
				writes := map[*ast.SelectorExpr]bool{}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if as, ok := n.(*ast.AssignStmt); ok {
						for _, l := range as.Lhs {
							if sel, ok := l.(*ast.SelectorExpr); ok {
								writes[sel] = true
							}
						}
					}
					return true
				})
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "live" || writes[sel] {
						return true
					}
					if !readers[fn.Name.Name] {
						offenders = append(offenders, "p.live is read in "+fn.Name.Name+" at "+fset.Position(sel.Pos()).String())
					}
					return true
				})
			}
		}
	}
	if len(offenders) > 0 {
		t.Errorf("the current run is looked up outside SetVolume/clearLive, so a reply could be routed to a run that did not produce it:\n%s",
			strings.Join(offenders, "\n"))
	}
}
