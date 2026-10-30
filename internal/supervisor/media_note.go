package supervisor

import "time"

// NoteMedia records that media is flowing through this run, for a child whose
// stdout is not the -progress parser. The meters and the loudness probes write
// their own data to stdout, so noteProgress never runs for them, mediaAt stayed
// zero, and their uptime read "--:--:--" while they metered a live stream.
//
// Only the first call per run stamps, as only the first progress block does;
// runOnce clears it on every respawn. It touches uptime and nothing else: the
// stall clock reads -progress output time, which these children do not report,
// and arming it would show every meter as stalled.
func (p *Process) NoteMedia() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mediaAt.IsZero() {
		p.mediaAt = time.Now()
	}
}
