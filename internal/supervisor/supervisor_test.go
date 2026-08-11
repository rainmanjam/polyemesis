// The process-lifecycle tests drive real children and probe whether their pids
// are still alive; both are platform-specific, and both live behind helpers in
// testfake_test.go so these bodies run unchanged on every platform we ship.
// The pure-logic tests (ring, classify, CommandString) live here too rather
// than in a second file, because splitting them buys nothing.

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// ------------------------------------------------------------------ restart

func TestAutoRestartRespawnsAfterNonZeroExit(t *testing.T) {
	rec := newRecorder()
	p := testProcess(t, fakeExit(1), Spec{
		AutoRestart: true,
		MinBackoff:  10 * time.Millisecond,
		MaxBackoff:  10 * time.Millisecond,
		OnState:     rec.onState,
	})
	p.Start()

	waitFor(t, "two restarts", func() bool { return p.Status().Restarts >= 2 })

	// Counting restarts is not enough: a supervisor that increments the
	// counter but never respawns would still pass that check.
	if got := rec.distinctPIDs(); got < 2 {
		t.Errorf("respawn must produce a new child; saw %d distinct pid(s)", got)
	}
	if !rec.saw(StateReconnecting) {
		t.Error("a restarting process must pass through reconnecting")
	}
	if got := p.Status().LastError; got == "" {
		t.Error("a non-zero exit must be reported as LastError")
	}
}

func TestNoAutoRestartIsTerminal(t *testing.T) {
	tests := []struct {
		name      string
		exitCode  int
		wantState State
		wantErr   bool
	}{
		{
			name:      "a failing child ends in failed and is not respawned",
			exitCode:  7,
			wantState: StateFailed,
			wantErr:   true,
		},
		{
			name:      "a child that exits cleanly ends in stopped",
			exitCode:  0,
			wantState: StateStopped,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := newRecorder()
			p := testProcess(t, fakeExit(tt.exitCode), Spec{
				AutoRestart: false,
				OnState:     rec.onState,
			})
			p.Start()

			waitFor(t, "terminal state", func() bool { return rec.saw(tt.wantState) })
			if !rec.saw(StateRunning) {
				t.Fatal("the child never reached running")
			}

			// Long enough for a backoff-free respawn loop to have gone round
			// several times if AutoRestart were being ignored.
			time.Sleep(100 * time.Millisecond)

			st := p.Status()
			if st.State != tt.wantState {
				t.Errorf("state = %q, want %q", st.State, tt.wantState)
			}
			if st.Restarts != 0 {
				t.Errorf("restarts = %d, want 0", st.Restarts)
			}
			if gotErr := st.LastError != ""; gotErr != tt.wantErr {
				t.Errorf("LastError = %q, want error: %v", st.LastError, tt.wantErr)
			}
			if st.PID != 0 {
				t.Errorf("PID = %d, want 0 once the child is gone", st.PID)
			}
		})
	}
}

func TestBackoffDoublesUpToTheCeiling(t *testing.T) {
	const (
		min = 20 * time.Millisecond
		max = 50 * time.Millisecond
	)
	// 20, 40, then clamped: 80 and 160 would both exceed the ceiling.
	want := []time.Duration{min, 2 * min, max, max}

	var mu sync.Mutex
	var got []time.Duration
	enough := make(chan struct{})

	p := testProcess(t, fakeExit(1), Spec{
		AutoRestart: true,
		MinBackoff:  min,
		MaxBackoff:  max,
		OnState: func(st Status) {
			if st.State != StateReconnecting {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if len(got) < len(want) {
				got = append(got, time.Duration(st.NextRetryIn*float64(time.Second)))
				if len(got) == len(want) {
					close(enough)
				}
			}
		},
	})
	p.Start()

	select {
	case <-enough:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out collecting retry delays")
	}

	mu.Lock()
	defer mu.Unlock()
	for i, w := range want {
		// NextRetryIn is computed a hair after the deadline is set, so it can
		// only ever read slightly short of the true backoff.
		if got[i] > w || got[i] < w-10*time.Millisecond {
			t.Errorf("retry %d delayed %v, want ~%v (sequence %v)", i+1, got[i], w, got)
		}
	}
	if got[1] <= got[0] {
		t.Errorf("backoff must grow between retries: %v", got)
	}
}

// --------------------------------------------------------------------- stop

// reapDeadline is the context deadline given to Stop by the test below, and it
// is load-bearing at a value nobody would guess.
//
// IT MUST EXCEED shutdownGrace (8s, supervisor.go:48). stop() signals the group
// and then selects on {child reaped, deadline}. When the signal does not land --
// a child that ignores SIGTERM, a Git-Bash `kill` that cannot deliver a real
// SIGTERM to a native .exe at all -- the ONLY thing that still ends the child is
// terminate()'s grace goroutine, which sleeps shutdownGrace and then kills the
// group. A deadline below 8s therefore expires BEFORE the rescue can fire, and
// the deadline arm is not merely possible but guaranteed: the test would be
// asserting the wrong arm every time, on every platform, whenever a signal is
// slow or ignored.
//
// That is what the old 3s value did, and why this test flaked on Windows
// (issue #180, "pid N survived Stop" on a byte-identical supervisor). Do not
// lower it back under 8s. Raising it costs nothing: it is a ceiling that a
// healthy run never approaches, paid in full only by a genuinely failing test,
// the same bargain waitTimeout makes in testfake_test.go.
const reapDeadline = 20 * time.Second

// reapHold, minReapLead and reapObserveBound are the three numbers the liveness
// half of the test below is built out of. They are an instrument, not padding,
// and the instrument is the point: it converts "Stop finished the reap" from a
// claim about internal control flow into an interval this test can measure.
//
// reapHold is how long the stdout drain is deliberately held open PAST the
// child's exit. runOnce (supervisor.go:744-745) does wg.Wait() BEFORE
// cmd.Wait(), so the supervisor cannot finish reaping until every pipe reader
// has returned; supervise() closes `done` only after runOnce returns; and stop()
// returns nil only on `case <-done`. Holding the drain therefore widens the gap
// between "the child died" and "Stop returned" to a value this test chose,
// exactly as fakeDeaf widens "the signal was ignored" into something observable.
//
// This is not an invented mechanism. It is the one issue #194 is about -- a
// descendant holding an inherited pipe makes `<-done` wait for the drain rather
// than for the child -- exercised deliberately, at a duration this test picks,
// instead of arriving unannounced in production.
//
// minReapLead is what the test then requires of that gap. THE MARGIN IS
// ONE-SIDED AND GUARANTEED BY time.Sleep: the true path's lead is at least
// reapHold, because the handler sleeps that long after EOF and everything else
// on the way back to the caller only adds to it. A slow or loaded machine can
// only make the lead LARGER, so this assertion has no flaky direction -- which
// is the property a bounded liveness poll never had.
//
// reapObserveBound is how long the test will wait, after Stop has returned, for
// the child's pipes to close at all. IT MUST STAY BELOW shutdownGrace (8s,
// supervisor.go:48) for the same reason the 2s bound in
// TestStopReportsWhenItHadToKillTheChild must: terminate()'s grace goroutine
// reaps an unheeded child at 8s on stop's behalf, so any bound at or past 8s is
// satisfied by the backstop rather than by Stop.
const (
	reapHold         = 1 * time.Second
	minReapLead      = 250 * time.Millisecond
	reapObserveBound = 2 * time.Second
)

// TestStopReapsTheChildWhenItHasTimeTo pins ONE of stop()'s two arms, by name.
//
// stop() has two exits and sets StateStopped on both:
//
//	case <-done:      supervise() finished cmd.Wait() -- the child IS reaped
//	case <-ctx.Done(): SIGKILL is issued and this returns without waiting;
//	                   returns ErrStopDeadline "may still be running"
//
// So the STATE cannot say which one ran. The error can, and it is the only
// thing that can, which is why this test captures it instead of discarding it.
// A test that throws it away cannot distinguish "Stop is broken" from "Stop
// correctly reported it ran out of time" -- the exact ambiguity that made #180
// take a day to read.
//
// AND THE ERROR IS NOT ENOUGH ON ITS OWN. An earlier revision of this test
// deleted the liveness check on the argument that err == nil is reachable only
// through `case <-done:`, which entails cmd.Wait() returned. That argument is
// about the code as written, and a test's job is to fail on code that is NOT as
// written. Measured against two mutations of stop(), the error assertion alone
// is green while the child is still running:
//
//	skip terminate(), the kill and the wait, return nil   -> PASS, child alive
//	signalGroup made inert + the reap arm returns nil     -> the whole package
//	                                                        is green, child alive
//
// Both are the hazard Stop's own doc comment names: the selector starts a
// replacement feed into the same hub the moment this returns, so a child that
// is still alive and still writing is two publishers on one input. So the
// liveness property is restored below -- but NOT as the check that used to be
// here, because the two objections to that check were both correct:
//
//  1. PID REUSE. alive(pid) is Kill(pid, 0) on Unix: between cmd.Wait() and the
//     probe the kernel may hand that pid to somebody else, so a true answer can
//     be about a process this test never started. It is also true for a zombie,
//     the precise state it was written to exclude.
//  2. VACUOUS PASS. Measured: a bounded 2s poll for !alive(pid) passes 10/10
//     against a stop() that kills and returns WITHOUT waiting -- the child dies
//     in about a millisecond, so the poll is satisfied by the kill rather than
//     by the reap. A check that a bug satisfies faster than the correct code
//     does is worse than no check.
//
// The assertion below answers both. It NAMES NO PID: the observation is the
// child's stdout pipe reaching EOF, which the kernel does by closing the fd at
// process teardown, and no recycled pid can hold that fd open. And it is not a
// poll for a state the mutant reaches sooner -- it is a MEASUREMENT OF THE
// ORDER of two timestamps, required to differ by at least minReapLead, a lead
// that only the full reap path can produce. See the constants above for why the
// margin cannot flake in the passing direction.
func TestStopReapsTheChildWhenItHasTimeTo(t *testing.T) {
	// childGoneAt receives the instant the child's stdout reached EOF, sent from
	// inside the supervisor's own drain goroutine. EOF on that pipe means every
	// writer closed it, and the only writer is the child, so this timestamp is
	// "the child process no longer exists" observed by the kernel rather than
	// inferred from a pid.
	//
	// The handler then SLEEPS, which is what makes the reap measurable: see
	// reapHold. A non-blocking send so a restart can never wedge the drain.
	childGoneAt := make(chan time.Time, 8)
	p := testProcess(t, fakeSleep(30*time.Second), Spec{
		AutoRestart: true,
		MinBackoff:  10 * time.Millisecond,
		StdoutHandler: func(r io.Reader) error {
			_, _ = io.Copy(io.Discard, r)
			select {
			case childGoneAt <- time.Now():
			default:
			}
			time.Sleep(reapHold)
			return nil
		},
	})
	p.Start()

	waitFor(t, "child to start", func() bool { return p.Status().State == StateRunning })
	if pid := p.Status().PID; pid == 0 {
		t.Fatal("a running process must report its pid")
	}

	ctx, cancel := context.WithTimeout(context.Background(), reapDeadline)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- p.Stop(ctx) }()

	var err error
	var stopReturnedAt time.Time
	select {
	case err = <-errc:
		stopReturnedAt = time.Now()
	case <-time.After(reapDeadline + 5*time.Second):
		t.Fatal("Stop did not return")
	}

	// ASSERTION ONE: WHICH ARM RAN. err == nil is reachable only through
	// `case <-done:`, and done is closed by supervise() after runOnce()
	// returned, which happens only after wg.Wait() and cmd.Wait() both
	// returned. Nothing else in this package can tell the reap arm from the
	// deadline arm -- the state is StateStopped on both -- so this assertion
	// stays, and stays first: it is the one that turns a failure into a
	// diagnosis instead of a symptom.
	//
	// The one caveat on the entailment: it reads "the tracked child was reaped"
	// only because this test established StateRunning first. Without that, Stop
	// on a process that never started also returns nil.
	if err != nil {
		t.Fatalf("Stop on a child that honours SIGTERM returned %v; this test pins the "+
			"reap path (case <-done), and a deadline here is a regression in the stop "+
			"path or an undersized deadline, not a slow runner. The deadline is %s and "+
			"the grace escalation that rescues an unheeded signal is %s.",
			err, reapDeadline, shutdownGrace)
	}

	// ASSERTION TWO: THE CHILD IS ACTUALLY GONE, AND STOP WAITED FOR IT.
	//
	// Assertion one is a statement about the code as written. This one holds
	// under rewriting, which is the only kind of assertion a regression test is
	// for. It is two claims, and each kills a different mutant:
	//
	//   the EOF arrived at all, within reapObserveBound
	//     -> kills "skip terminate, the kill and the wait, return nil" and
	//        "signalGroup inert + the reap arm returns nil": in both the child
	//        is still running when Stop returns and its stdout stays open --
	//        for ever in the first case, until the 8s grace backstop in the
	//        second, and reapObserveBound is under 8s precisely so the backstop
	//        cannot answer for stop().
	//
	//   the EOF preceded Stop's return by at least minReapLead
	//     -> kills "kill the child and return nil without waiting", the mutant
	//        that a bounded liveness poll passes 10/10 because a SIGKILLed
	//        child dies in about a millisecond. There the order INVERTS: Stop
	//        returns first and the EOF lands after it, so the lead is negative
	//        against a requirement of +250ms.
	//
	// No pid appears in either claim, so neither can be satisfied by a pid the
	// kernel handed to somebody else, and neither is confused by a zombie: an
	// unreaped-but-dead child has already had its fds closed.
	var childGone time.Time
	select {
	case childGone = <-childGoneAt:
	case <-time.After(reapObserveBound):
		t.Fatalf("Stop returned nil, but %s later the child's stdout is STILL OPEN, so the "+
			"child is still running. Stop must reap the child, not merely stop watching "+
			"it: the selector starts a replacement feed into the same hub the moment this "+
			"returns, and a leaked FFmpeg keeps holding the capture device and the "+
			"destination socket. The bound is deliberately under the %s grace escalation, "+
			"so a pass here cannot be the backstop answering on stop's behalf AFTER Stop "+
			"returned -- it says nothing about the wait INSIDE Stop, where the backstop "+
			"legitimately can and does answer (measured: signalGroup made inert and "+
			"nothing else, this test passes at 9.11s instead of 1.11s because the grace "+
			"goroutine reaps within Stop's own deadline). That gap is inherent, because "+
			"the deadline must exceed the grace for the deadline arm to be reachable at "+
			"all; TestStopReportsWhenItHadToKillTheChild is what covers it.",
			reapObserveBound, shutdownGrace)
	}
	// Drain any later EOF (a restart would produce one) and keep the last, so
	// the lead below is measured against the child Stop actually reaped.
	for draining := true; draining; {
		select {
		case childGone = <-childGoneAt:
		default:
			draining = false
		}
	}
	if lead := stopReturnedAt.Sub(childGone); lead < minReapLead {
		t.Fatalf("the child's stdout reached EOF only %s before Stop returned (want at least "+
			"%s). Stop returned on something other than the completed reap -- it issued a "+
			"kill and did not wait, or it stopped waiting early. The drain in this test "+
			"holds runOnce open for %s past the child's exit, and stop() blocks on `done`, "+
			"which supervise() closes only after runOnce returns; a correct Stop therefore "+
			"cannot show a lead below that. A negative lead means Stop returned BEFORE the "+
			"child was gone.", lead, minReapLead, reapHold)
	}

	if st := p.Status(); st.State != StateStopped {
		t.Errorf("state = %q, want %q", st.State, StateStopped)
	}
	// AutoRestart must not resurrect a deliberately stopped process.
	time.Sleep(100 * time.Millisecond)
	if st := p.Status(); st.State != StateStopped {
		t.Errorf("state after Stop = %q, want it to stay %q", st.State, StateStopped)
	}
}

// --------------------------------------------------------------------- logs

func TestStderrCaptureIsBoundedAndOldestFirst(t *testing.T) {
	const emitted = logRingSize + 200

	rec := newRecorder()
	p := testProcess(t, fakeStderr(emitted), Spec{
		AutoRestart: false,
		OnState:     rec.onState,
	})
	p.Start()

	waitFor(t, "child to finish", func() bool { return rec.saw(StateStopped) })

	logs := p.Logs()
	if len(logs) != logRingSize {
		t.Fatalf("captured %d lines, want the ring capacity %d", len(logs), logRingSize)
	}
	if want := fmt.Sprintf("line%d", emitted-logRingSize); logs[0].Text != want {
		t.Errorf("oldest retained line = %q, want %q", logs[0].Text, want)
	}
	if want := fmt.Sprintf("line%d", emitted-1); logs[len(logs)-1].Text != want {
		t.Errorf("newest line = %q, want %q", logs[len(logs)-1].Text, want)
	}
	if logs[0].Process != p.Name() {
		t.Errorf("log line process = %q, want %q", logs[0].Process, p.Name())
	}
}

func TestRingSnapshotOrdersOldestFirst(t *testing.T) {
	tests := []struct {
		name  string
		size  int
		added int
		want  []string
	}{
		{name: "an untouched ring is empty", size: 4, added: 0, want: nil},
		{name: "a partly filled ring yields only what was added", size: 4, added: 2, want: []string{"0", "1"}},
		{name: "a ring filled exactly to capacity does not drop its first line", size: 4, added: 4, want: []string{"0", "1", "2", "3"}},
		{name: "one line past capacity evicts only the oldest", size: 4, added: 5, want: []string{"1", "2", "3", "4"}},
		{name: "a wrapped ring reads oldest to newest across the seam", size: 4, added: 6, want: []string{"2", "3", "4", "5"}},
		{name: "several laps still leave exactly the last capacity lines", size: 4, added: 13, want: []string{"9", "10", "11", "12"}},
		{name: "a single-slot ring keeps only the newest", size: 1, added: 3, want: []string{"2"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRing(tt.size)
			for i := 0; i < tt.added; i++ {
				r.add(LogLine{Text: fmt.Sprint(i)})
			}

			got := r.snapshot()
			if len(got) != len(tt.want) {
				t.Fatalf("snapshot has %d lines, want %d", len(got), len(tt.want))
			}
			for i, w := range tt.want {
				if got[i].Text != w {
					t.Errorf("line %d = %q, want %q (got %v)", i, got[i].Text, w, texts(got))
				}
			}
		})
	}
}

func TestRingSnapshotIsADetachedCopy(t *testing.T) {
	r := newRing(2)
	r.add(LogLine{Text: "first"})

	snap := r.snapshot()
	r.add(LogLine{Text: "second"})
	r.add(LogLine{Text: "third"})

	if len(snap) != 1 || snap[0].Text != "first" {
		t.Errorf("snapshot changed under the caller: %v", texts(snap))
	}
}

func texts(lines []LogLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Text
	}
	return out
}

// ----------------------------------------------------------------- classify

func TestClassifyLevels(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{"fatal outranks the error it also contains", "[out#0] Fatal error initialising muxer", "fatal"},
		{"a panic is fatal", "assertion failed: PANIC in mov muxer", "fatal"},
		{"a refused connection is an error", "rtmp://a.rtmp.example/live: Connection refused", "error"},
		{"a missing file is an error", "srt.sdp: No such file or directory", "error"},
		{"an unwritable output is an error", "Unable to open output file", "error"},
		{"an invalid argument is an error", "Invalid sample rate 0", "error"},
		{"a failed operation is an error", "Encoder initialization failed", "error"},
		{"deprecation is only a warning", "The pixel format yuvj420p is deprecated", "warning"},
		{"past duration is only a warning", "Past duration 0.998253 too large", "warning"},
		{"non-monotonous DTS is only a warning", "Non-monotonous DTS in output stream 0:1", "warning"},
		{"an explicit warning is a warning", "[hls] WARNING: skipping segment", "warning"},
		{"a progress line is plain info", "frame= 1200 fps= 25 q=28.0 size=  4096kB", "info"},
		{"a banner line is plain info", "ffmpeg version 7.1 Copyright (c) 2000-2024", "info"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classify(tt.line); got != tt.want {
				t.Errorf("classify(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}

// ------------------------------------------------------------ CommandString

func TestCommandStringQuoting(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "a bare binary renders alone",
			want: "ffmpeg",
		},
		{
			name: "ordinary flags are left unquoted",
			args: []string{"-i", "srt://0.0.0.0:6000", "-c:v", "copy"},
			want: "ffmpeg -i srt://0.0.0.0:6000 -c:v copy",
		},
		{
			name: "an argument containing a space is quoted",
			args: []string{"-i", "/media/My Recordings/take 1.mkv"},
			want: "ffmpeg -i '/media/My Recordings/take 1.mkv'",
		},
		{
			name: "a filter graph with pipes is quoted",
			args: []string{"-filter_complex", "[0:a]pan=stereo|c0=c0|c1=c1[out]"},
			want: "ffmpeg -filter_complex '[0:a]pan=stereo|c0=c0|c1=c1[out]'",
		},
		{
			name: "an embedded single quote is escaped, not swallowed",
			args: []string{"-metadata", "title=Ben's show"},
			want: `ffmpeg -metadata 'title=Ben'\''s show'`,
		},
		{
			name: "shell metacharacters are quoted even without spaces",
			args: []string{"rtmp://x/live;rm", "$HOME"},
			want: "ffmpeg 'rtmp://x/live;rm' '$HOME'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(slog.New(slog.NewTextHandler(io.Discard, nil)),
				Spec{Name: "ingest", Bin: "ffmpeg", Args: tt.args})
			if got := p.CommandString(); got != tt.want {
				t.Errorf("CommandString() = %s, want %s", got, tt.want)
			}
		})
	}
}

// A destination that retries forever looks exactly like one that works: the
// card says "reconnecting", the supervisor is busy, and nothing ever says the
// endpoint has refused us forty times and is not coming back.
func TestGivingUpAfterTooManyConsecutiveRestarts(t *testing.T) {
	rec := newRecorder()
	p := testProcess(t, fakeExit(1), Spec{
		AutoRestart: true,
		MaxRestarts: 3,
		MinBackoff:  5 * time.Millisecond,
		MaxBackoff:  5 * time.Millisecond,
		OnState:     rec.onState,
	})
	p.Start()

	waitFor(t, "the supervisor to give up", func() bool {
		return p.Status().State == StateFailed
	})

	st := p.Status()
	// StateFailed, not StateStopped: stopped is what an operator asked for,
	// failed is what happened to them, and only one of those is an incident.
	if st.State != StateFailed {
		t.Fatalf("state = %s, want %s", st.State, StateFailed)
	}
	if !strings.Contains(st.LastError, "gave up after") {
		t.Errorf("LastError = %q, want it to say the supervisor gave up", st.LastError)
	}
	if st.Restarts > 3 {
		t.Errorf("restarted %d times with MaxRestarts=3", st.Restarts)
	}

	// And it must STAY given up. A supervisor that quietly resumed would be
	// worse than one that never stopped, because the operator has now been
	// told it is dead.
	before := p.Status().Restarts
	time.Sleep(60 * time.Millisecond)
	if after := p.Status().Restarts; after != before {
		t.Errorf("restarts went from %d to %d after giving up", before, after)
	}
}

// The default, and what every process here did before the limit existed. The
// ingest in particular must never give up: a publisher that reconnects after
// an hour is normal operation.
func TestZeroMaxRestartsRetriesForever(t *testing.T) {
	p := testProcess(t, fakeExit(1), Spec{
		AutoRestart: true,
		MinBackoff:  5 * time.Millisecond,
		MaxBackoff:  5 * time.Millisecond,
	})
	p.Start()

	waitFor(t, "several restarts", func() bool { return p.Status().Restarts >= 5 })
	if st := p.Status().State; st == StateFailed {
		t.Errorf("state = %s with MaxRestarts unset; it must retry forever", st)
	}
}

// StartDelay holds the FIRST spawn back and nothing else. A destination that
// drops at 3am has to come back immediately, not wait its turn behind
// processes that are already healthy.
//
// The "and nothing else" half is measured DIFFERENTIALLY, against the same
// three restarts run with no start delay at all, and the reason is the whole
// point of this comment.
//
// Three restarts are three real process spawns. A spawn costs single-digit
// milliseconds here and ~95ms on a GitHub Windows runner, whose speed varies
// about 3x across the pool. The version of this test that compared the elapsed
// time of those three restarts against ONE 120ms delay therefore failed on
// Windows -- 288ms measured -- while the supervisor was behaving correctly: it
// was reading platform spawn cost as evidence the delay had been reapplied.
// That is a false failure on a correct implementation, which is the worst kind
// of test, and no absolute budget can fix it, because there is no number that
// is both above Windows spawn cost and below the signal on a fast Linux box.
//
// Subtracting a no-delay run cancels the spawn cost, because both runs pay it.
// What survives the subtraction is the delay, or nothing.
func TestStartDelayAppliesToTheFirstSpawnOnly(t *testing.T) {
	const delay = 200 * time.Millisecond

	// restartsAfterTheFirst times the three restarts that FOLLOW the first
	// spawn. The start delay is paid before the first spawn, so it is outside
	// this window by construction; what is inside is spawn cost plus backoff.
	// A correct supervisor returns the same duration for any startDelay.
	restartsAfterTheFirst := func(startDelay time.Duration) time.Duration {
		t.Helper()
		begin := time.Now()
		p := testProcess(t, fakeExit(1), Spec{
			AutoRestart: true,
			StartDelay:  startDelay,
			MinBackoff:  5 * time.Millisecond,
			MaxBackoff:  5 * time.Millisecond,
		})
		p.Start()
		// Stopped as soon as its window closes rather than left to t.Cleanup:
		// a process still restarting through the other run's measurement is
		// spawn contention landing exactly on the number being read.
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			p.Stop(ctx)
		}()

		waitFor(t, "the first spawn", func() bool { return p.Status().Restarts >= 1 })
		if waited := time.Since(begin); waited < startDelay {
			t.Errorf("first spawn happened after %s, want at least the %s start delay",
				waited, startDelay)
		}

		afterFirst := time.Now()
		waitFor(t, "three more restarts", func() bool { return p.Status().Restarts >= 4 })
		return time.Since(afterFirst)
	}

	baseline := restartsAfterTheFirst(0)
	withDelay := restartsAfterTheFirst(delay)

	// A delay applied per restart would add THREE of them to the window, so
	// the regression this guards against shows up as +600ms. The threshold is
	// two delays: comfortably below the 3x a regression adds, and far above
	// the run-to-run spawn variance the subtraction leaves behind (~100ms at
	// the Windows numbers in #103). Both margins are deliberate -- a threshold
	// at exactly 3 delays would sit on the regression boundary, and one at a
	// single delay would be back within variance.
	if extra := withDelay - baseline; extra > 2*delay {
		t.Errorf("three restarts took %s with a %s start delay against %s without one, "+
			"%s more than the same three spawns cost on this machine: the delay is "+
			"being applied to reconnects", withDelay, delay, baseline, extra)
	}
}

// A stop that arrives during the stagger window must not wait it out.
func TestStartDelayIsInterruptedByAStop(t *testing.T) {
	p := testProcess(t, fakeSleep(30*time.Second), Spec{
		AutoRestart: true,
		StartDelay:  10 * time.Second,
	})
	p.Start()

	begin := time.Now()
	p.Stop(context.Background())
	if waited := time.Since(begin); waited > 2*time.Second {
		t.Errorf("Stop took %s; it waited out the start delay instead of cancelling it", waited)
	}
}

// A CHILD THAT HAD TO BE KILLED IS NOT A CHILD THAT STOPPED, and Stop has to
// say which happened.
//
// Both outcomes end at StateStopped, so the state cannot answer it. The caller
// that needs the difference is the selector: teardownFeed returns and ensureFeed
// immediately starts a replacement feed into the same hub, so a child still
// alive and still writing means two publishers on one input -- a corrupted
// timeline rather than a missing one. Before this, the only record was a log
// line, which nothing can branch on. See issue #138.
func TestStopReportsWhenItHadToKillTheChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		// NOT "no SIGTERM semantics on Windows" -- that was false, and being
		// false it made the deadline arm look like a Unix curiosity when it is
		// the arm Windows takes in PRODUCTION: under the SCM (service_windows.go
		// :50-82) a stop request arrives with a deadline already ticking.
		//
		// The real reason to skip is that arm selection here is not yet proven
		// deterministic on a console-less runner. signalGroup's Windows path is
		// GenerateConsoleCtrlEvent (proc_windows.go:66-74), and when that fails
		// -- which it does with no attached console -- it falls straight
		// through to killGroup. TerminateProcess is asynchronous by contract,
		// but the child may still be reaped fast enough for `done` to race the
		// already-expired ctx at the select, and this test would then fail
		// claiming Stop reported a clean stop on an expired deadline.
		//
		// Preconditions for un-skipping, recorded so the next person does not
		// have to rediscover them: prove deterministic arm selection on a
		// console-less runner, and land the fakeDeaf readiness handshake first
		// (done: see testfake_test.go) so "deaf" is verified rather than
		// assumed. Tracked as its own issue; deliberately not gambled on the
		// day this repository is prosecuting flakes.
		t.Skip("deadline-arm selection is not yet proven deterministic on a console-less Windows runner")
	}
	p := testProcess(t, fakeDeaf(30*time.Second), Spec{})
	p.Start()
	waitFor(t, "child to start", func() bool { return p.Status().State == StateRunning })
	// StateRunning only means cmd.Start() returned. Wait for the child to say
	// its handlers are up, or "deaf" is an aspiration.
	waitForDeaf(t, p)
	pid := p.Status().PID

	// AN ALREADY-EXPIRED DEADLINE, and the reason is worth recording.
	//
	// The comment that used to sit here credited the context-aware exec
	// constructor with killing the child on cancellation. This package does not
	// use it: runOnce builds children with plain exec.Command (supervisor.go
	// :671) and sets no WaitDelay, so cancelling the process's own context
	// signals nothing by itself, and the old comment described a mechanism that
	// is not present. (Grep the package for the constructor's name and the only
	// hit was that comment.) The true mechanism is narrower: signalGroup asks, and
	// the ONLY thing that follows up on a child that does not listen is
	// terminate()'s grace goroutine, which sleeps shutdownGrace (8s) and then
	// kills the group. Any deadline shorter than that reaches the select first.
	//
	// So an already-expired context forces the deadline arm deterministically,
	// which is what this test wants: when the deadline DOES win -- a child
	// whose exit is blocked on something SIGKILL cannot hurry, a wedged pipe
	// with a grandchild holding it open -- Stop must say so instead of
	// reporting a clean stop. A spent deadline exercises exactly that select
	// arm without pretending to reproduce the wedge.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := p.Stop(ctx)

	if err == nil {
		t.Fatal("Stop reported a clean stop on an expired deadline; the selector starts a " +
			"replacement feed into the same hub on the strength of that answer")
	}
	if !errors.Is(err, ErrStopDeadline) {
		t.Errorf("err = %v, want ErrStopDeadline so a caller can tell this from any other failure", err)
	}
	if st := p.Status(); st.State != StateStopped {
		t.Errorf("state = %q, want %q: the process is retired either way", st.State, StateStopped)
	}
	// The kill still has to land, or this reports a problem while also leaking
	// the child.
	//
	// THE BOUND IS 2s AND THAT IS THE WHOLE POINT OF THE ASSERTION. waitFor's
	// 15s ceiling made this test pass for the wrong reason, measured: with
	// p.kill() deleted from the deadline arm, the poll still went green at
	// ~17s of wall clock, because terminate()'s grace goroutine reaps the child
	// at shutdownGrace (8s) on stop's behalf. A leak check that any 8s backstop
	// satisfies is not checking the kill this arm is documented to issue.
	//
	// 2s is below shutdownGrace by enough that only stop()'s own p.kill() can
	// satisfy it, and far above the ~1ms a SIGKILL'd fake child actually takes.
	// If this ever needs raising, it may not go to or past 8: it would stop
	// distinguishing the kill from the grace period and become the ninth test
	// in this repository that passes for a reason nobody intended.
	//
	// alive() is used HERE AND NOWHERE ELSE, as a leak check, never as a Stop
	// postcondition -- see the comment in TestStopReapsTheChildWhenItHasTimeTo
	// for the two ways it lies. Here its weakness does not matter: the failing
	// direction (a pid that stays alive) is the one being excluded, and 2s of
	// pid reuse on a just-killed child is not a hazard worth trading the
	// mutation sensitivity for.
	waitForBounded(t, 2*time.Second, "the killed child to be reaped by stop's own kill",
		func() bool { return !alive(pid) })
}

// And the ordinary path must stay quiet, or the selector would log a warning on
// every healthy switch and the signal would be worthless.
func TestStopReportsNoErrorWhenTheChildExitsOnItsOwn(t *testing.T) {
	p := testProcess(t, fakeSleep(30*time.Second), Spec{})
	p.Start()
	waitFor(t, "child to start", func() bool { return p.Status().State == StateRunning })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Errorf("Stop on a child that honours SIGTERM returned %v, want nil", err)
	}
}
