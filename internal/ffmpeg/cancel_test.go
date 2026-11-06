package ffmpeg

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

/* CANCELLATION HUNG FOR EVER ON A GRANDCHILD THAT OUTLIVED ITS PARENT.
 *
 * KillGrace exists for exactly one scenario, and says so: "a child that has
 * spawned its own children -- which x265 and SVT-AV1 both do -- can leave those
 * pipes open after the parent is gone. Without WaitDelay the worker would sit in
 * Wait forever and the queue's cancellation would be a lie."
 *
 * WaitDelay only has effect INSIDE Cmd.Wait. It bounds how long Wait spends on
 * the I/O pipes after the process has exited, and then closes them itself. Exec
 * blocked on the reader goroutines FIRST, so in that scenario the readers never
 * saw EOF, wg.Wait never returned, and Wait was never reached to apply the
 * delay. The mitigation was unreachable from the case it was written for.
 *
 * Measured against the previous ordering: this test hung past 25 seconds. With
 * Wait first it returns in about 300ms.
 */
func TestCancellingACommandDoesNotHangOnASurvivingGrandchild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		// The helper spawns a grandchild that inherits stdout and outlives it.
		// Cancelling kills the helper; the grandchild holds the pipe open, which
		// is the shape of an encoder that forks its own workers.
		done <- Exec(ctx, helper("grandchild"), Sink{})
	}()

	// Long enough for the grandchild to exist and the reader to be blocked.
	time.Sleep(300 * time.Millisecond)
	cancel()

	// Generous against KillGrace (5s) and against a loaded CI runner, while
	// still far below the unbounded wait this pins.
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled command reported success")
		}
		if !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("cancelled command failed with %q, want the context's own "+
				"error — \"signal: killed\" tells an operator nothing", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Exec did not return 20s after cancellation. The reader " +
			"goroutines are being waited on before Cmd.Wait, so WaitDelay — the " +
			"whole reason KillGrace exists — never gets the chance to close the " +
			"pipes a surviving grandchild is holding open.")
	}
}

// execGoroutines counts goroutines running a closure defined inside Exec: the
// two pipe readers and the one that waits for them. Counted by stack rather
// than by runtime.NumGoroutine so the rest of the test binary cannot move it.
func execGoroutines() int {
	var buf []byte
	for size := 1 << 20; ; size *= 2 {
		buf = make([]byte, size)
		if n := runtime.Stack(buf, true); n < size {
			buf = buf[:n]
			break
		}
	}
	count := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "internal/ffmpeg.execWithGrace.func") {
			count++
		}
	}
	return count
}

// EXEC MUST NOT LEAVE ITS READERS BEHIND WHEN THE DRAIN TIMES OUT.
//
// A surviving grandchild holds the output pipes open, so the readers never see
// EOF and the drain gives up after KillGrace. Wait then closes the pipes, which
// wakes the readers -- but nothing joined them, so Exec returned while up to
// three goroutines were still unwinding. The check is made the instant Exec
// returns, with no polling, because "they will be gone in a moment" is the
// leak.
func TestExecJoinsItsReaderGoroutinesAfterADrainTimeout(t *testing.T) {
	const grace = 400 * time.Millisecond

	// The stderr reader is parked INSIDE its sink callback, which is what makes
	// the leak observable on demand instead of by luck: the reader cannot have
	// finished unless Exec waited for it. It is released after Wait has closed
	// the pipes but inside the join's grace.
	entered := make(chan struct{})
	release := make(chan struct{})
	var once, releaseOnce sync.Once
	free := func() { releaseOnce.Do(func() { close(release) }) }
	// Closed on every exit, so a test that fails early cannot leave the reader
	// parked for the tests after it.
	t.Cleanup(free)
	sink := Sink{Line: func(string) {
		once.Do(func() { close(entered) })
		<-release
	}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- execWithGrace(ctx, helper("grandchild"), sink, grace) }()

	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("the child never wrote a line, so the reader was never parked")
	}
	// Sit through one whole grace first, so the drain has certainly timed out
	// and Exec is inside Wait however slowly the child started. Cancelling then
	// closes the pipes at once; the join has `grace` from that moment, and the
	// reader is released halfway through it.
	time.Sleep(grace + grace/2)
	cancel()
	time.AfterFunc(grace/2, free)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled command reported success")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Exec did not return after cancellation")
	}
	if n := goroutinesGone(); n != 0 {
		t.Fatalf("%d goroutine(s) started by Exec are still running after it returned", n)
	}
}

// A drain that finishes normally must not be delayed by the join.
func TestExecJoinAddsNoWaitToAChildThatExitsCleanly(t *testing.T) {
	start := time.Now()
	if err := Exec(context.Background(), helper("progress"), Sink{}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if d := time.Since(start); d > KillGrace {
		t.Fatalf("a clean child took %v, longer than the grace", d)
	}
	if n := goroutinesGone(); n != 0 {
		t.Fatalf("%d goroutine(s) started by Exec are still running after a clean exit", n)
	}
}

// goroutinesGone is execGoroutines after letting goroutines that have already
// signalled completion finish popping their frames. A goroutine parked inside a
// callback never gets there, so a real leak still fails; only the few
// microseconds between "signalled" and "exited" are forgiven.
func goroutinesGone() int {
	n := execGoroutines()
	for i := 0; i < 50 && n != 0; i++ {
		time.Sleep(time.Millisecond)
		n = execGoroutines()
	}
	return n
}
