//go:build !windows

// The live command channel's stdin path. Unix only, and by design rather than
// by omission: on Windows the supervisor gives a destination no stdin pipe
// (runOnce), every SetVolume answers errLiveUnavailable, and the engine
// restarts on every level change instead. There is no path here to exercise
// on Windows; live_windows_test.go proves the refusal that replaces it.

package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

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
