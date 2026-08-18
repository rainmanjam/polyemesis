package media

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

/* CANCELLATION HUNG FOR EVER ON A GRANDCHILD THAT OUTLIVED ITS PARENT.
 *
 * killGrace exists for exactly one scenario, and says so: "a child that has
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
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("no sh on this platform: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		// sh spawns a grandchild that inherits stdout and outlives it. Cancelling
		// kills sh; the grandchild holds the pipe open, which is the shape of an
		// encoder that forks its own workers.
		done <- Exec(ctx, Command{
			Name: "sh",
			Args: []string{"-c", "sleep 30 & echo started; sleep 30"},
		}, Sink{})
	}()

	// Long enough for the grandchild to exist and the reader to be blocked.
	time.Sleep(300 * time.Millisecond)
	cancel()

	// Generous against killGrace (5s) and against a loaded CI runner, while
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
			"whole reason killGrace exists — never gets the chance to close the " +
			"pipes a surviving grandchild is holding open.")
	}
}
