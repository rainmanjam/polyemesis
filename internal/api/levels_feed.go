package api

import "time"

// LEVELS FEED STATE: whether the meter readings a caller holds are current.
//
// A Stream Deck dial (or any meter) is drawn from /levels or the WebSocket's
// levels frames. When the meters stop, the last reading does not stop being
// returned, so a dial would keep drawing a bar that is no longer true. The
// server states the age in one word so a client can show "NO FEED" instead.
//
// The window is three meter intervals, but never less than one second: a
// slow-polling client at a 40 ms interval should not flap between live and
// stale on ordinary scheduling jitter.
//
// States:
//
//	none  - no reading has ever arrived in this run of the engine
//	live  - the newest reading is inside the window
//	stale - readings arrived and have stopped
const (
	levelsFeedNone  = "none"
	levelsFeedLive  = "live"
	levelsFeedStale = "stale"

	// levelsFeedFloor is the least the window is ever allowed to be.
	levelsFeedFloor = time.Second
)

// levelsFeedState names the state of a reading taken at at, judged at now, for
// a metering interval of interval.
func levelsFeedState(at, now time.Time, interval time.Duration) string {
	if at.IsZero() {
		return levelsFeedNone
	}
	window := 3 * interval
	if window < levelsFeedFloor {
		window = levelsFeedFloor
	}
	if now.Sub(at) <= window {
		return levelsFeedLive
	}
	return levelsFeedStale
}
