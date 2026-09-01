// Uptime is measured from the first media, not from the spawn.
//
// THE BUG THIS PINS. An ingest is spawned LISTENING: FFmpeg binds the SRT or
// RTMP port and blocks until an encoder connects, so the process exists, has a
// pid, and is Running from the moment it can accept a connection. UptimeSec was
// derived from startedAt, which is that moment -- so the dashboard and the
// header clock counted the wait for OBS as airtime. An operator who armed the
// ingest at breakfast and went live at noon was told the stream had been up for
// four hours.
//
// These drive the Process struct directly rather than through a child, because
// what is under test is the arithmetic in Status() and the single assignment in
// the progress callback. Spawning a real FFmpeg that waits for a connection
// would test the operating system's socket layer and take a wall-clock second
// to say the same thing.

package supervisor

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// running builds a Process in the state a spawned-but-silent child is in: pid
// assigned, state Running, startedAt set, no media yet.
func running(startedAgo time.Duration) *Process {
	p := &Process{}
	p.state = StateRunning
	p.pid = 4242
	p.startedAt = time.Now().Add(-startedAgo)
	return p
}

func TestUptimeIsZeroWhileAnIngestIsOnlyListening(t *testing.T) {
	// The whole bug in one assertion: a process alive for an hour with no media
	// has not been up for an hour.
	p := running(time.Hour)

	st := p.Status()

	if st.State != StateRunning {
		t.Fatalf("state = %v, want Running -- a listening ingest is running", st.State)
	}
	if st.UptimeSec != 0 {
		t.Errorf("UptimeSec = %v, want 0: no media has arrived, so nothing is up", st.UptimeSec)
	}
	if st.StartedAt == nil {
		t.Error("StartedAt = nil, want the spawn time: it still answers how long the process has existed")
	}
}

func TestUptimeRunsFromTheFirstMediaNotTheSpawn(t *testing.T) {
	p := running(time.Hour)

	// Media arrives now, an hour after the spawn.
	p.noteProgress(ffmpeg.Progress{OutTimeMS: 40})

	st := p.Status()
	if st.UptimeSec > 5 {
		t.Errorf("UptimeSec = %v, want ~0: media has just started, the preceding hour was waiting", st.UptimeSec)
	}
	if st.StartedAt == nil || time.Since(*st.StartedAt) < 59*time.Minute {
		t.Error("StartedAt should still report the spawn, an hour ago")
	}
}

func TestProgressWithoutMediaDoesNotStartTheClock(t *testing.T) {
	// FFmpeg emits progress blocks before anything moves. A block whose output
	// timestamp has not advanced is the encoder saying "still nothing", and
	// treating it as media would reintroduce the bug through a narrower door.
	p := running(time.Hour)

	p.noteProgress(ffmpeg.Progress{Frame: 0, OutTimeMS: 0, TotalSize: 0})

	if got := p.Status().UptimeSec; got != 0 {
		t.Errorf("UptimeSec = %v, want 0: an empty progress block is not media", got)
	}
}

func TestAudioOnlyMediaStartsTheClock(t *testing.T) {
	// Frame stays zero for an audio-only destination, which is why the signal is
	// OutTimeMS and not Frame. Getting this wrong would leave audio-only
	// destinations reporting zero uptime forever.
	p := running(time.Minute)

	p.noteProgress(ffmpeg.Progress{Frame: 0, OutTimeMS: 1000, Speed: 1})

	// ASSERT THE CLOCK STARTED, NOT THAT TIME HAS PASSED. An earlier version
	// checked UptimeSec > 0 microseconds after the stamp and failed on Windows,
	// whose timer granularity is coarse enough that time.Since can return
	// exactly zero. The claim being tested is "audio-only media starts the
	// clock", and mediaAt being set is that claim; the derived seconds are a
	// race against the platform's clock resolution.
	p.mu.Lock()
	started := !p.mediaAt.IsZero()
	p.mu.Unlock()
	if !started {
		t.Error("mediaAt is still zero: audio-only media did not start the clock")
	}
}

func TestTheClockDoesNotRestartOnLaterProgress(t *testing.T) {
	p := running(time.Minute)
	p.noteProgress(ffmpeg.Progress{OutTimeMS: 40})
	p.mu.Lock()
	p.mediaAt = time.Now().Add(-30 * time.Minute) // pretend it has been live half an hour
	p.mu.Unlock()

	// Every subsequent block must leave the start alone; stamping it again would
	// peg uptime near zero forever, which reads as a stream that never stays up.
	p.noteProgress(ffmpeg.Progress{OutTimeMS: 1_800_000})

	if got := p.Status().UptimeSec; got < 60 {
		t.Errorf("UptimeSec = %v, want ~1800: later progress must not restamp the start", got)
	}
}

func TestRespawnRestartsTheClock(t *testing.T) {
	// A reconnect is a new stream as far as every platform downstream is
	// concerned. Carrying the old total across would hide the gap -- the number
	// would say an hour when the audience saw a break in the middle of it.
	p := running(time.Minute)
	p.noteProgress(ffmpeg.Progress{OutTimeMS: 40})
	p.mu.Lock()
	p.mediaAt = time.Now().Add(-30 * time.Minute)
	// What runOnce does on a fresh spawn.
	p.startedAt = time.Now()
	p.mediaAt = time.Time{}
	p.progress = ffmpeg.Progress{}
	p.mu.Unlock()

	if got := p.Status().UptimeSec; got != 0 {
		t.Errorf("UptimeSec = %v, want 0 after a respawn: the new run has carried no media yet", got)
	}
}

func TestUptimeIsZeroWhenNotRunning(t *testing.T) {
	p := running(time.Minute)
	p.noteProgress(ffmpeg.Progress{OutTimeMS: 40})
	p.mu.Lock()
	p.mediaAt = time.Now().Add(-30 * time.Minute)
	p.state = StateReconnecting
	p.mu.Unlock()

	if got := p.Status().UptimeSec; got != 0 {
		t.Errorf("UptimeSec = %v, want 0 while reconnecting: it is not on air", got)
	}
}
