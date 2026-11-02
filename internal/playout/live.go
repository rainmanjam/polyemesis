package playout

import "time"

// LiveState is what the public page says about the stream.
type LiveState string

const (
	// LiveStateLive means segments are arriving.
	LiveStateLive LiveState = "live"
	// LiveStateEnded means a broadcast was on air recently and has stopped.
	// Distinct from offline so the page can say "the broadcast has ended"
	// rather than "nothing is on yet".
	LiveStateEnded LiveState = "ended"
	// LiveStateOffline means nothing has been on air recently.
	LiveStateOffline LiveState = "offline"
)

const (
	// liveScanTTL bounds how often LiveState walks the segment directory. The
	// public page is polled by every viewer; one walk every two seconds serves
	// them all.
	liveScanTTL = 2 * time.Second
	// minLiveGrace is the floor on how stale the newest segment may be and still
	// count as live. A rung restart leaves the ladder empty for a moment, and
	// flipping a watching viewer to "ended" for that would be wrong.
	minLiveGrace = 15 * time.Second
	// EndedHold is how long "ended" is shown before the page falls back to
	// "offline".
	EndedHold = 12 * time.Hour
)

// LiveState reports whether the stream is on air, judged by the newest segment
// on disk rather than by whether a muxer process exists: a muxer can be up
// with no ingest feeding it, and a viewer cares about pictures, not processes.
// It also returns when the stream was last seen live (zero if never in this
// process).
func (m *Manager) LiveState() (LiveState, time.Time) {
	now := m.now()
	m.mu.Lock()
	seg := m.settings.SegmentSeconds
	m.mu.Unlock()
	if seg <= 0 {
		seg = 4
	}
	grace := 3 * time.Duration(seg) * time.Second
	if grace < minLiveGrace {
		grace = minLiveGrace
	}

	m.liveMu.Lock()
	if m.liveScanned.IsZero() || now.Sub(m.liveScanned) >= liveScanTTL {
		m.liveScanned = now
		m.liveMu.Unlock()
		newest := newestSegmentIn(m.dir)
		m.liveMu.Lock()
		if newest.After(m.newestSeg) {
			m.newestSeg = newest
		}
	}
	last := m.newestSeg
	m.liveMu.Unlock()

	return classifyLive(now, last, grace), last
}

// classifyLive is LiveState's decision, as a pure function of the clock.
func classifyLive(now, lastSegment time.Time, grace time.Duration) LiveState {
	switch {
	case lastSegment.IsZero():
		return LiveStateOffline
	case now.Sub(lastSegment) <= grace:
		return LiveStateLive
	case now.Sub(lastSegment) <= EndedHold:
		return LiveStateEnded
	}
	return LiveStateOffline
}

// noteNewest records a segment time seen outside LiveState -- a teardown is
// about to clear the directory -- so the ended state survives the clearing.
func (m *Manager) noteNewest(t time.Time) {
	m.liveMu.Lock()
	if t.After(m.newestSeg) {
		m.newestSeg = t
	}
	m.liveMu.Unlock()
}

// newestSegmentIn is the newest segment's modification time under dir, zero
// when there is none.
func newestSegmentIn(dir string) time.Time {
	files, err := scanSegments(dir)
	if err != nil {
		return time.Time{}
	}
	var newest time.Time
	for _, f := range files {
		if f.mod.After(newest) {
			newest = f.mod
		}
	}
	return newest
}
