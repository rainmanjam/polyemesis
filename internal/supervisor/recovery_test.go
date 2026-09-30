// LiveForSec answers "how long has this run's delivery been unbroken",
// measured from the most recent recovery rather than from the run's first
// media.
//
// THE BUG THIS PINS. internal/engine's sourceLiveFor used UptimeSec for a
// failover feed's "how long has the source been back" -- and UptimeSec runs
// from mediaAt for the whole process run, with no notion of an intervening
// stall. A feed that freezes and recovers WITHOUT its process restarting (a
// dropout the failover tier rides out on the same feed) kept reporting an
// uptime from its very first media, long before the outage, the instant it
// started moving again. destinationStalled read that stale, large number as
// "the source has been back for ages" and skipped the grace period it exists
// to give a destination still catching up from the outage, so that
// destination was immediately called stalled on its own account. LiveForSec
// is the field that answers the question UptimeSec was being asked to answer
// and cannot: it resets to the moment of recovery, not just the moment of
// first media.
package supervisor

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// TestLiveForSecResetsOnARecoveryWithoutARespawn is the mutation-checked
// regression: revert the LiveForSec wiring in noteProgress/Status and this
// fails because LiveForSec keeps reporting time since the run's first media
// straight through the stall, the same bug UptimeSec has always had by
// design (see uptime_test.go) and that is wrong for THIS field's job.
func TestLiveForSecResetsOnARecoveryWithoutARespawn(t *testing.T) {
	p := running(0)
	p.stallAfter = 200 * time.Millisecond

	// First media: liveSince starts here, same instant as mediaAt.
	p.noteProgress(ffmpeg.Progress{OutTimeMS: 40})

	// Pretend this run has been live, unbroken, for ten minutes.
	p.mu.Lock()
	p.mediaAt = time.Now().Add(-10 * time.Minute)
	p.liveSince = p.mediaAt
	p.movedAt = time.Now().Add(-10 * time.Minute)
	p.mu.Unlock()

	// Then it stalls: no further progress for well over stallAfter.
	p.mu.Lock()
	p.movedAt = time.Now().Add(-time.Second) // > 200ms stallAfter
	p.mu.Unlock()
	if st := p.Status(); !st.Stalled {
		t.Fatalf("setup: expected the process to read stalled before recovery, got %+v", st)
	}

	// It recovers -- media moves again -- WITHOUT the process restarting:
	// no respawn, mediaAt is untouched.
	p.noteProgress(ffmpeg.Progress{OutTimeMS: 80})

	st := p.Status()
	if st.Stalled {
		t.Fatalf("Stalled = true immediately after recovery, want false: %+v", st)
	}
	// UptimeSec, unchanged by design, still reports the whole run -- roughly
	// ten minutes. LiveForSec must NOT: the run just recovered from a stall,
	// so it has been live, unbroken, for a moment, not ten minutes.
	if st.UptimeSec < 9*60 {
		t.Fatalf("UptimeSec = %v, want ~600s: it answers a different question and must not have moved", st.UptimeSec)
	}
	if st.LiveForSec > 5 {
		t.Fatalf("LiveForSec = %v, want ~0: the run just recovered from a stall, "+
			"it has not been live for ten minutes", st.LiveForSec)
	}
}

// TestLiveForSecRunsFromFirstMediaWhenNeverStalled is the control: a run that
// never stalled has LiveForSec and UptimeSec agree, because the "most recent
// recovery" is the same instant as the first media.
func TestLiveForSecRunsFromFirstMediaWhenNeverStalled(t *testing.T) {
	p := running(0)
	p.stallAfter = time.Minute

	p.noteProgress(ffmpeg.Progress{OutTimeMS: 40})
	p.mu.Lock()
	p.mediaAt = time.Now().Add(-5 * time.Minute)
	p.liveSince = p.mediaAt
	p.movedAt = time.Now()
	p.mu.Unlock()

	// One more ordinary block, output time advancing, no gap near stallAfter.
	p.noteProgress(ffmpeg.Progress{OutTimeMS: 80})

	st := p.Status()
	if st.LiveForSec < 4*60 {
		t.Errorf("LiveForSec = %v, want ~300s: nothing has stalled, so it should track UptimeSec", st.LiveForSec)
	}
}
