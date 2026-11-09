package db

import "fmt"

// Stop-on-ingest-loss bounds.
//
// THE FLOOR IS ABOVE EVERY ENGINE TIMER THAT CAN LOOK LIKE A LOSS AND RECOVER:
//
//   - ingestLiveGrace, 3s: two missed bitrate samples.
//   - the hooks watcher's DefaultDisconnectAfter, 5s, and supervisor.StallAfter, 5s.
//   - alerts.DefaultDownFor, 20s: how long the ingest must be silent before
//     ingest.lost is raised. The alert therefore always pages BEFORE this
//     fallback can stop anything.
//   - supervisor's default MaxBackoff, 30s: the longest an RTMP or pull ingest
//     child waits before it is respawned after the publisher dropped. A
//     publisher that reconnects ("rtmp publisher disconnected" followed by an
//     ingest process restart in the staging log) is back inside this plus the
//     encoder's own retry, so 60s is twice the worst respawn.
//   - the default failover grace, 5s. The configured grace can be as long as
//     MaxFailoverGraceSeconds (300s), which no fixed floor can sit above while
//     the default stays at 120s, so the ENGINE also never acts before the
//     programme's configured failover grace plus AutoStopFailoverMarginSeconds:
//     see AutoStopSettings.EffectiveWait.
//
// THE CEILING, one hour, is MaxStopGraceSeconds' for the same reason: past it
// the operator is not describing a safety net but a schedule. The default is
// two minutes: long enough to ride out a reconnect, a tunnel flap or an
// encoder restart, short enough that a dead backpack does not hold a platform
// slot, and a YouTube broadcast open, for a quarter of an hour.
const (
	MinAutoStopWaitSeconds     = 60
	DefaultAutoStopWaitSeconds = 120
	MaxAutoStopWaitSeconds     = 3600
	// AutoStopFailoverMarginSeconds is the clearance the engine keeps above a
	// programme's configured failover grace.
	AutoStopFailoverMarginSeconds = 30
)

// AutoStopSettings is a programme's stop-on-ingest-loss fallback (a safety net
// for any encoder: OBS with the bridge off or failing, vMix, hardware, an IRL
// backpack).
//
// OFF BY DEFAULT, and armed only by Enabled AND Acknowledged: stopping a
// destination ends its YouTube broadcast for good (see the file header of
// internal/api/destinations_bulk.go), so the operator says so once, in writing,
// the way POST /destinations/stop-all makes every caller say it per request.
// The OBS bridge's stopAcknowledged does NOT cover this: it is the consent to
// stop WHEN OBS STOPS, a different trigger, and an install that gave it has not
// agreed to a stop that nobody at OBS asked for.
//
// ONE SETTING. Settings, OBS bridge shows and edits this same value for the
// bridge's programme; there is no second timer and no second copy.
type AutoStopSettings struct {
	Enabled bool `json:"enabled"`
	// WaitSeconds is how long neither the primary nor the backup ingest may
	// deliver before the programme's live destinations stop. 0 means the default.
	WaitSeconds int `json:"waitSeconds"`
	// Acknowledged is the operator's consent; see the type.
	Acknowledged bool `json:"acknowledged"`
}

// AutoStopAckText is the sentence a refused save carries. It is stop-all's
// warning, in its words.
const AutoStopAckText = "stopping destinations when the ingest is lost ends any live YouTube broadcast " +
	"among them, permanently -- starting them again puts video back on the wire but does not " +
	`bring the broadcast back. Set "acknowledged": true once that is intended`

// Armed reports whether the fallback may stop anything. Both halves, so a row
// written by hand or by another build with the acknowledgement missing stops
// nothing rather than everything.
func (a AutoStopSettings) Armed() bool { return a.Enabled && a.Acknowledged }

// Wait is WaitSeconds with 0 read as the default.
func (a AutoStopSettings) Wait() int {
	if a.WaitSeconds == 0 {
		return DefaultAutoStopWaitSeconds
	}
	return a.WaitSeconds
}

// EffectiveWait is the wait the engine actually uses: never below the floor and
// never below the programme's configured failover grace plus the margin, so a
// grace raised after the wait was saved cannot make the fallback act inside the
// selector's own decision window. failoverGraceSeconds is 0 when failover is off.
func (a AutoStopSettings) EffectiveWait(failoverGraceSeconds int) int {
	w := a.Wait()
	if w < MinAutoStopWaitSeconds {
		w = MinAutoStopWaitSeconds
	}
	if failoverGraceSeconds > 0 && w < failoverGraceSeconds+AutoStopFailoverMarginSeconds {
		w = failoverGraceSeconds + AutoStopFailoverMarginSeconds
	}
	return w
}

func (a AutoStopSettings) problems() []string {
	var probs []string
	if a.WaitSeconds != 0 && (a.WaitSeconds < MinAutoStopWaitSeconds || a.WaitSeconds > MaxAutoStopWaitSeconds) {
		probs = append(probs, fmt.Sprintf("auto-stop wait %ds out of range (%d-%d)",
			a.WaitSeconds, MinAutoStopWaitSeconds, MaxAutoStopWaitSeconds))
	}
	if a.Enabled && !a.Acknowledged {
		probs = append(probs, AutoStopAckText)
	}
	return probs
}
