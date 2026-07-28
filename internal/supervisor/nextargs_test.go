package supervisor

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// NextArgs exists for one reason: a destination writing to a FILE cannot re-run
// the command that produced the file it is already holding. FFmpeg refuses an
// existing output and exits, so before this the first restart ended the
// destination permanently — it crash-looped on "already exists" forever, and
// the recording stayed at zero bytes.
//
// A respawn is therefore not always a repeat, and these tests pin the two
// things that has to mean.

func TestNextArgsIsCalledForEverySpawn(t *testing.T) {
	var mu sync.Mutex
	var seen [][]string

	base := fakeStderr(1)
	p := testProcess(t, base, Spec{
		AutoRestart: true,
		MinBackoff:  10 * time.Millisecond,
		MaxBackoff:  10 * time.Millisecond,
	})
	// Set after testProcess, which fills Bin/Args from the fake.
	p.spec.NextArgs = func() []string {
		mu.Lock()
		defer mu.Unlock()
		// A distinct argv per spawn, standing in for a distinct output path.
		args := []string{fakeChildFlag, "stderr", strconv.Itoa(len(seen) + 1)}
		seen = append(seen, args)
		return args
	}
	p.Start()

	waitFor(t, "three spawns", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) >= 3
	})

	mu.Lock()
	defer mu.Unlock()
	// Every spawn must have got its OWN argv. If the supervisor cached the
	// first result, a file destination would be back to reusing a path FFmpeg
	// has already refused.
	uniq := map[string]bool{}
	for _, a := range seen {
		uniq[strings.Join(a, " ")] = true
	}
	if len(uniq) != len(seen) {
		t.Fatalf("%d spawns produced only %d distinct argv; NextArgs is being cached",
			len(seen), len(uniq))
	}
}

func TestCommandStringShowsTheArgvActuallyRunning(t *testing.T) {
	// The monitoring page and the crash-loop debug line both read this. If they
	// showed the argv the process was constructed with, an operator diagnosing
	// a restart loop would be looking at a command that no longer exists —
	// which is precisely the situation NextArgs creates.
	resolved := []string{fakeChildFlag, "stderr", "7"}
	p := testProcess(t, fakeStderr(1), Spec{
		AutoRestart: true,
		MinBackoff:  10 * time.Millisecond,
		MaxBackoff:  10 * time.Millisecond,
	})
	configured := strings.Join(p.spec.Args, " ")
	p.spec.NextArgs = func() []string { return resolved }

	// Exact argv, not a substring: the binary lives under a temp path full of
	// digits, and a needle like "7" matches it by accident. The first version
	// of this test passed for that reason and proved nothing.
	if got := strings.Join(p.currentArgs(), " "); got != configured {
		t.Fatalf("before any spawn, the configured argv is the only honest answer, got %q", got)
	}

	p.Start()
	want := strings.Join(resolved, " ")
	waitFor(t, "a spawn", func() bool { return strings.Join(p.currentArgs(), " ") == want })

	if got := strings.Join(p.Args(), " "); !strings.HasSuffix(got, want) {
		t.Fatalf("Args() must report the running argv too, got %q", got)
	}
	if got := p.CommandString(); !strings.HasSuffix(got, want) {
		t.Fatalf("CommandString() must report the running argv, got %q", got)
	}
}

func TestNilNextArgsUsesTheConfiguredArgs(t *testing.T) {
	// The overwhelmingly common case: an RTMP or SRT destination is reconnected
	// to, not recreated, so nothing should change about how it respawns.
	p := testProcess(t, fakeStderr(2), Spec{
		AutoRestart: true,
		MinBackoff:  10 * time.Millisecond,
		MaxBackoff:  10 * time.Millisecond,
	})
	want := strings.Join(p.spec.Args, " ")
	p.Start()

	waitFor(t, "a restart", func() bool { return p.Status().Restarts >= 1 })

	if got := strings.Join(p.currentArgs(), " "); got != want {
		t.Fatalf("argv changed without NextArgs set:\n got %q\nwant %q", got, want)
	}
}
