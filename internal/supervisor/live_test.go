package supervisor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
// nil or a wrong descriptor.
func TestSetVolumeRefusesAProcessThatDidNotOptIn(t *testing.T) {
	p := New(quietLog(), Spec{Name: "plain", Bin: "/bin/sh", Args: []string{"-c", "sleep 5"}})
	p.Start()
	defer func() { _ = p.Stop(t.Context()) }()
	waitFor(t, "running", func() bool { return p.Status().State == StateRunning })

	if err := p.SetVolume("volume@trk0", 0.5); err != errLiveUnavailable {
		t.Fatalf("SetVolume on a non-opted-in process = %v, want errLiveUnavailable", err)
	}
}

// The success path: the line reaches the child exactly as built, and a reply
// with ret:0 is what the caller is told.
func TestSetVolumeWritesOneCommandAndAcceptsRetZero(t *testing.T) {
	dir := t.TempDir()
	got := filepath.Join(dir, "line")
	script := `read line; printf '%s' "$line" > "$0.out"; echo "Command reply for stream 0: ret:0 res:" >&2; sleep 30`
	p := New(quietLog(), Spec{
		Name: "live", Bin: "/bin/sh", Args: []string{"-c", script, got}, LiveCommands: true,
	})
	p.Start()
	defer func() { _ = p.Stop(t.Context()) }()
	waitFor(t, "running", func() bool { return p.Status().State == StateRunning })

	if err := p.SetVolume("volume@trk0", 0.5); err != nil {
		t.Fatalf("SetVolume: %v", err)
	}
	b, err := os.ReadFile(got + ".out")
	if err != nil {
		t.Fatalf("the child never received the command: %v", err)
	}
	if string(b) != "c"+"volume@trk0 -1 volume 0.5" {
		t.Errorf("child received %q", string(b))
	}
}

// FFmpeg answers a rejected command with a non-zero ret. That must come back
// as an error so the engine restarts instead of believing the level changed.
func TestSetVolumeReportsARejectionAsAnError(t *testing.T) {
	script := `read line; echo "Command reply for stream 0: ret:-78 res:" >&2; sleep 30`
	p := New(quietLog(), Spec{Name: "live", Bin: "/bin/sh", Args: []string{"-c", script}, LiveCommands: true})
	p.Start()
	defer func() { _ = p.Stop(t.Context()) }()
	waitFor(t, "running", func() bool { return p.Status().State == StateRunning })

	if err := p.SetVolume("volume@trk0", 0.5); err == nil {
		t.Fatal("a ret:-78 rejection was reported as success")
	}
}

// No reply inside the deadline is a failure, and it poisons the run. The child
// answers the first command LATE, inside the second command's window. A run
// that was not poisoned would take that stale ret:0 as the second command's
// answer; a poisoned run refuses the second command without writing it.
func TestSetVolumeTimeoutPoisonsTheRun(t *testing.T) {
	withReplyTimeout(t, 200*time.Millisecond)
	script := `read line; sleep 0.3; echo "Command reply for stream 0: ret:0 res:" >&2; while read l; do :; done`
	p := New(quietLog(), Spec{Name: "live", Bin: "/bin/sh", Args: []string{"-c", script}, LiveCommands: true})
	p.Start()
	defer func() { _ = p.Stop(t.Context()) }()
	waitFor(t, "running", func() bool { return p.Status().State == StateRunning })

	start := time.Now()
	if err := p.SetVolume("volume@trk0", 0.5); err != errLiveTimeout {
		t.Fatalf("first SetVolume = %v, want errLiveTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout took %v, far beyond the %v deadline", elapsed, liveReplyTimeout)
	}
	if err := p.SetVolume("volume@trk0", 0.6); err != errLiveUnavailable {
		t.Fatalf("second SetVolume after a timeout = %v, want errLiveUnavailable: a late reply to the first command must not answer the second", err)
	}
}

// One command at a time: two concurrent callers each get their own reply and
// their lines do not interleave on the pipe.
func TestSetVolumeCallersDoNotInterleave(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "lines")
	script := `while read line; do printf '%s\n' "$line" >> "$0"; echo "Command reply for stream 0: ret:0 res:" >&2; done`
	p := New(quietLog(), Spec{Name: "live", Bin: "/bin/sh", Args: []string{"-c", script, out}, LiveCommands: true})
	p.Start()
	defer func() { _ = p.Stop(t.Context()) }()
	waitFor(t, "running", func() bool { return p.Status().State == StateRunning })

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- p.SetVolume("volume@trk0", float64(i)/10)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent SetVolume: %v", err)
		}
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 8 {
		t.Fatalf("child read %d lines, want 8: %q", len(lines), lines)
	}
	for _, l := range lines {
		// The leading `c` is FFmpeg's command key, which the fake child sees too.
		if !strings.HasPrefix(l, "cvolume@trk0 -1 volume ") {
			t.Errorf("a line arrived interleaved or malformed: %q", l)
		}
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
func TestOnlyAWholeReplyLineIsAnAcknowledgement(t *testing.T) {
	good := []string{
		"Command reply for stream 0: ret:0 res:",
		"Command reply for stream 0: ret:0",
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

// THE STREAM INDEX IS CHECKED, not just parsed. FFmpeg sends a `c` command to
// every filter graph and prints one reply per graph, numbered by graph. A
// destination has exactly one, its -filter_complex, which is graph 0. A reply
// numbered anything else means a second graph answered, so which reply is ours
// is ambiguous, and an ambiguous answer is a refusal: the caller restarts.
//
// MUTATION: in send, drop the `rep.stream != liveGraphIndex` arm. Observed:
// FAIL, a stream-1 ret:0 was taken as success.
func TestAReplyFromAnotherGraphIsNotAnAcknowledgement(t *testing.T) {
	withReplyTimeout(t, 500*time.Millisecond)
	script := `read line; echo "Command reply for stream 1: ret:0 res:" >&2; while read l; do :; done`
	p := New(quietLog(), Spec{Name: "live", Bin: "/bin/sh", Args: []string{"-c", script}, LiveCommands: true})
	p.Start()
	defer func() { _ = p.Stop(t.Context()) }()
	waitFor(t, "running", func() bool { return p.Status().State == StateRunning })

	if err := p.SetVolume("volume@trk0", 0.5); err == nil {
		t.Fatal("a reply from filter graph 1 acknowledged a command meant for graph 0")
	}
	// And the run is spent: whatever graph 0 says later cannot be trusted to
	// belong to the next command.
	if err := p.SetVolume("volume@trk0", 0.6); err != errLiveUnavailable {
		t.Fatalf("after an ambiguous reply SetVolume = %v, want errLiveUnavailable", err)
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
