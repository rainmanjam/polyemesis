package supervisor

import (
	"testing"
	"time"
)

// A child whose stdout is not the -progress parser -- the meters and the
// loudness probes write their own data there -- never reaches noteProgress, so
// mediaAt stayed zero for its whole life and the monitoring page read its uptime
// as "--:--:--" while it metered a live stream. NoteMedia is how such a handler
// says "media is flowing".
func TestNoteMediaStartsUptimeForAChildWithItsOwnStdout(t *testing.T) {
	p := running(time.Hour)
	p.NoteMedia()
	time.Sleep(20 * time.Millisecond)

	st := p.Status()
	if st.UptimeSec <= 0 {
		t.Fatalf("UptimeSec = %v after NoteMedia, want > 0", st.UptimeSec)
	}
	if st.UptimeSec > 60 {
		t.Errorf("UptimeSec = %v: it ran from the spawn an hour ago, want from the first media", st.UptimeSec)
	}
}

// Only the first call stamps, exactly as only the first progress block does:
// restamping per frame would peg uptime near zero.
func TestNoteMediaKeepsTheFirstStamp(t *testing.T) {
	p := running(time.Hour)
	p.NoteMedia()
	first := p.mediaAt
	time.Sleep(5 * time.Millisecond)
	p.NoteMedia()
	if !p.mediaAt.Equal(first) {
		t.Errorf("a second NoteMedia moved mediaAt from %v to %v", first, p.mediaAt)
	}
}

// Uptime only: the stall clock belongs to -progress output time, which these
// children do not report. Turning it on would read every meter as stalled five
// seconds in.
func TestNoteMediaDoesNotArmTheStallClock(t *testing.T) {
	p := running(time.Hour)
	p.stallAfter = time.Millisecond
	p.NoteMedia()
	time.Sleep(10 * time.Millisecond)
	if st := p.Status(); st.Stalled {
		t.Error("NoteMedia armed stall detection for a child that reports no output time")
	}
}
