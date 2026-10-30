//go:build windows

package supervisor

import (
	"testing"
	"time"
)

// ON WINDOWS A LIVE LEVEL IS ALWAYS REFUSED, and the refusal is what makes the
// engine restart instead. The live path is unverified there, so runOnce gives an
// opted-in destination no stdin pipe at all, and SetVolume must answer
// errLiveUnavailable rather than write anywhere. This is the Windows half of
// live_unix_test.go, which covers the pipe that exists everywhere else.
//
// MUTATION: drop the `runtime.GOOS != "windows"` guard in runOnce. Observed
// (by reading, on a Windows runner): the child gets a pipe and SetVolume blocks
// for the reply timeout or writes into the fake's stdin, never answering
// errLiveUnavailable.
func TestOnWindowsSetVolumeIsRefusedEvenWhenOptedIn(t *testing.T) {
	p := testProcess(t, fakeSleep(30*time.Second), Spec{Name: "live", LiveCommands: true})
	p.Start()
	waitFor(t, "running", func() bool { return p.Status().State == StateRunning })

	if err := p.SetVolume("volume@trk0", 0.5); err != errLiveUnavailable {
		t.Fatalf("SetVolume on Windows = %v, want errLiveUnavailable so the engine restarts", err)
	}
}
