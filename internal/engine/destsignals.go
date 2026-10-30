package engine

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// The per-destination signals that sit beside DestStatus.Reason: a restart
// storm, dropped frames, the connected account's health and the platform's own
// verdict on the stream. Each one is a fact the server already holds -- the
// supervisor's respawns and progress, the account book behind GET
// /platforms/accounts, a liveStreams read -- turned into one field here, so
// that every client (the dashboard, the Stream Deck key) reads the same verdict
// and none of them has to make one up.

// destinationRestartStorm is DestStatus.RestartStorm: the respawns of the last
// supervisor.RestartStormWindow, when there are more than RestartStormAbove of
// them, and 0 otherwise. The same `> 5 in 15m` as PolyemesisDestinationRestartStorm.
//
// Not for a stopped destination: Stop is the operator's, and a destination they
// stopped after a storm is not storming.
func destinationRestartStorm(p *supervisor.Status) int {
	if p == nil || p.State == supervisor.StateStopped || p.RecentRestarts <= supervisor.RestartStormAbove {
		return 0
	}
	return p.RecentRestarts
}

// droppingFramesPercent is the share of the window's frames that must have been
// dropped before a destination is called dropping. One percent: a stray frame
// at a keyframe boundary is not a fault, a steady one in a hundred is visible.
const droppingFramesPercent = 1

// destinationDropping is DestStatus.DroppingFrames: the frames FFmpeg dropped
// over the last supervisor.RateWindow, when that is at least
// droppingFramesPercent of the frames it output, and 0 otherwise.
//
// Drops only, not dups. A drop is FFmpeg discarding to keep up with its output;
// a dup is FFmpeg padding because the source is starving, which is the source's
// problem and is reported as one. Judged only once the source has been arriving
// for a whole window, the rule destinationReason keeps for a slow upload: frames
// lost around an ingest's return are one ingest event, not a fault per platform.
func destinationDropping(p *supervisor.Status, ingestConfigured bool, sourceLive time.Duration) int64 {
	if p == nil || p.State != supervisor.StateRunning || !p.DropsKnown || p.DroppedRecent <= 0 {
		return 0
	}
	if ingestConfigured && sourceLive < supervisor.RateWindow {
		return 0
	}
	if p.DroppedRecent*100 < p.FramesRecent*droppingFramesPercent {
		return 0
	}
	return p.DroppedRecent
}

// AccountHealthReader reads a connected account's health: the same state word
// GET /platforms/accounts carries as health.state, "ok", "expiring",
// "refreshFailing" or "expired", and "" when the account is unknown.
//
// An interface for KeyChecker's reason: the account book lives in internal/api,
// which imports this package. IT MUST NOT BLOCK ON THE NETWORK. It is called
// from Status, per WebSocket push, for every destination with an account.
type AccountHealthReader interface {
	AccountHealth(accountID int64) string
}

// SetAccountHealth attaches the account health reader. Nil-safe, like
// SetKeyChecker.
func (e *Engine) SetAccountHealth(r AccountHealthReader) {
	e.mu.Lock()
	e.accountHealthReader = r
	e.mu.Unlock()
}

// accountHealth is DestStatus.AccountHealth: the destination's connected
// account's state word when it is not "ok", and "" otherwise. ONLY THE WORD.
// The refresh error and the failure count stay on GET /platforms/accounts,
// which is admin-scope; this payload is read-scope.
func (e *Engine) accountHealth(row *db.Destination) string {
	if row == nil || row.AccountID == nil {
		return ""
	}
	e.mu.RLock()
	r := e.accountHealthReader
	e.mu.RUnlock()
	if r == nil {
		return ""
	}
	switch h := r.AccountHealth(*row.AccountID); h {
	case "expiring", "refreshFailing", "expired":
		return h
	default:
		return ""
	}
}

// PlatformHealth is the platform's own verdict on a destination's stream:
// for YouTube, liveStreams status.healthStatus. Reported on DestStatus only
// when it is poor (see platformHealthPoor).
type PlatformHealth struct {
	Platform db.Platform `json:"platform"`
	// Status is the platform's word, verbatim: YouTube's good, ok, bad or
	// noData. "good" is BETTER than "ok" (docs/evidence/platform-lifecycle-
	// apis-2026-08-16.md), the reverse of the usual order.
	Status string `json:"status"`
	// Issues are the platform's codes for the configuration issues it rates
	// as errors, e.g. YouTube's "bitrateLow". Codes, not prose: the platform's
	// own descriptions are English, and a code is what a client can translate.
	Issues []string `json:"issues,omitempty"`
}

// platformHealthPoor is the one verdict: "bad", or any error-severity issue.
// noData is not poor. YouTube reports it for a stream it has not judged yet,
// which says nothing about the stream.
func platformHealthPoor(h PlatformHealth) bool {
	return h.Status == "bad" || len(h.Issues) > 0
}

// PlatformHealthChecker reads a destination's stream health from its platform.
// ok=false means the question cannot be asked for this destination (no
// account, no stream with this key) and is not an error.
//
// READ-ONLY, for KeyChecker's reason, and through the destination's own
// connected account only.
type PlatformHealthChecker interface {
	StreamHealth(ctx context.Context, destID int64) (h PlatformHealth, ok bool, err error)
}

// SetPlatformHealth attaches the platform health reader. Nil-safe.
func (e *Engine) SetPlatformHealth(c PlatformHealthChecker) {
	e.mu.Lock()
	e.platformHealthChecker = c
	e.mu.Unlock()
}

// platformHealthEvery is how often a live destination's platform health is
// read. YouTube's liveStreams.list costs one quota unit a page (see
// ytKeyPagesMax in internal/oauth/streamkeys.go), so at this cadence one live
// destination spends 288 units a day, under 3% of a default project's 10,000,
// and the api side shares one read between every destination on a channel.
// Chat, which paces itself against the same allowance (internal/chat/quota.go),
// is the reason this is not faster: a health badge that starved the chat pane
// would be switched off, and rightly.
const platformHealthEvery = 5 * time.Minute

// platformHealthTimeout bounds one read.
const platformHealthTimeout = 20 * time.Second

// healthCheckState is one destination's platform reading, for one stored key.
type healthCheckState struct {
	key      [32]byte
	reading  *PlatformHealth
	inflight bool
	at       time.Time
}

// platformHealth is DestStatus.PlatformHealth: the last poor reading for a
// destination that is delivering to YouTube through a connected account, and
// nil otherwise. NEVER BLOCKS, for keyChanged's reason: the first Status of a
// run reports nothing, and the read publishes a fresh status when it lands.
//
// Twitch is not asked because there is nothing to ask: it has no public
// stream-health API (docs/DESIGN-DESTINATION-HEALTH.md). A destination that is
// not delivering -- off, between runs, or running with no media yet -- has
// nothing for the platform to judge, so it is not asked either, and its last
// reading is forgotten: a verdict about an earlier run is not one about the
// next.
func (e *Engine) platformHealth(row *db.Destination, p *supervisor.Status) *PlatformHealth {
	if row == nil {
		return nil
	}
	live := p != nil && p.State == supervisor.StateRunning && p.UptimeSec > 0
	if !live || row.AccountID == nil || row.StreamKey == "" || row.Platform != db.PlatformYouTube {
		e.healthMu.Lock()
		delete(e.healthChecks, row.ID)
		e.healthMu.Unlock()
		return nil
	}
	e.mu.RLock()
	c := e.platformHealthChecker
	e.mu.RUnlock()
	if c == nil {
		return nil
	}
	digest := sha256.Sum256([]byte(row.StreamKey))
	e.healthMu.Lock()
	defer e.healthMu.Unlock()
	if e.healthChecks == nil {
		e.healthChecks = map[int64]*healthCheckState{}
	}
	st := e.healthChecks[row.ID]
	if st == nil || st.key != digest {
		st = &healthCheckState{key: digest}
		e.healthChecks[row.ID] = st
	}
	if !st.inflight && (st.at.IsZero() || time.Since(st.at) >= platformHealthEvery) {
		st.inflight = true
		go e.runHealthCheck(c, row.ID, digest)
	}
	if st.reading == nil {
		return nil
	}
	h := *st.reading
	return &h
}

// runHealthCheck reads once and records the answer against the key it was
// asked about. A failed read keeps the previous answer and is not retried
// before platformHealthEvery, so a flaky platform API cannot make the key
// flicker, and cannot spend quota on retries.
func (e *Engine) runHealthCheck(c PlatformHealthChecker, destID int64, digest [32]byte) {
	ctx, cancel := context.WithTimeout(context.Background(), platformHealthTimeout)
	defer cancel()
	h, ok, err := c.StreamHealth(ctx, destID)
	if err != nil {
		e.log.Warn("platform stream health read failed", "dest", destID, "err", err)
	}
	changed := false
	e.healthMu.Lock()
	st := e.healthChecks[destID]
	if st != nil && st.key == digest {
		st.inflight = false
		st.at = time.Now()
		if err == nil {
			var next *PlatformHealth
			if ok && platformHealthPoor(h) {
				next = &h
			}
			changed = (next == nil) != (st.reading == nil)
			st.reading = next
		}
	}
	e.healthMu.Unlock()
	if changed && e.bus != nil {
		e.publishStatus()
	}
}
