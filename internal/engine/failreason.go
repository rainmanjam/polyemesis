package engine

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/failreason"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// destHoldAfterQuickRejections is how many quick key or codec refusals in a row
// hold a destination (supervisor.Spec.HoldAfterQuickRejections). Three, as
// agreed for #20: one refusal can be a platform blip, two in a row within
// seconds is already a pattern, and the third is where retrying stops being
// hope and starts being noise against an ingest that has answered.
const destHoldAfterQuickRejections = 3

// KeyChecker asks a destination's connected platform account whether the
// stream key the destination is publishing with is still one of that
// account's keys.
//
// An interface because the implementation needs OAuth tokens, providers and
// the credential store, all of which live in internal/api -- which imports this
// package. Set with SetKeyChecker; every use is nil-safe.
//
// IT MUST BE READ-ONLY. It runs because a publish was refused, and a check that
// minted a broadcast or rotated a stream to answer would move the key under an
// encoder that the operator is about to fix by hand. See oauth.StreamKeyReader.
type KeyChecker interface {
	// StreamKeyChanged reports changed=true when the platform's current keys
	// do not include the destination's stored key. ok=false means the
	// question cannot be answered for this destination -- no connected
	// account, or a platform with no read-only key endpoint -- and is not an
	// error.
	StreamKeyChanged(ctx context.Context, destID int64) (changed, ok bool, err error)
}

// SetKeyChecker attaches the cross-check. A setter for SetLifecycle's reason:
// engines are created whenever a source is added, long after main built the
// API server that implements it.
func (e *Engine) SetKeyChecker(k KeyChecker) {
	e.mu.Lock()
	e.keyChecker = k
	e.mu.Unlock()
}

func (e *Engine) keyCheckerOrNil() KeyChecker {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.keyChecker
}

// keyCheckState is one destination's cross-check, for one stored key.
type keyCheckState struct {
	// key identifies the stored key the answer is about, as a digest so the
	// cache never holds a second plaintext copy. A key edited or refreshed
	// since is a different question, and the cached answer is dropped.
	key      [32]byte
	changed  bool
	inflight bool
	at       time.Time
}

// keyCheckEvery is how long an answer is trusted before it is asked again while
// the refusals continue. Long enough that a destination refused every few
// seconds does not turn into a platform API call every few seconds -- YouTube
// meters liveStreams.list against a daily quota -- and short enough that an
// operator who rotated the key in Studio while the card was open sees it.
const keyCheckEvery = 5 * time.Minute

// keyCheckTimeout bounds one cross-check.
const keyCheckTimeout = 20 * time.Second

// keyChanged reports what the cross-check last said about row's key, and starts
// a check in the background when there is no current answer.
//
// NEVER BLOCKS. It is called from Status, which runs per WebSocket push, and the
// check is a network round trip to a platform. The first Status after a refusal
// reports "not known yet"; the check publishes a fresh status when it lands.
// One check per destination at a time.
func (e *Engine) keyChanged(row *db.Destination) bool {
	if row == nil || row.AccountID == nil || row.StreamKey == "" {
		return false
	}
	k := e.keyCheckerOrNil()
	if k == nil {
		return false
	}
	digest := sha256.Sum256([]byte(row.StreamKey))
	e.keyMu.Lock()
	defer e.keyMu.Unlock()
	if e.keyChecks == nil {
		e.keyChecks = map[int64]*keyCheckState{}
	}
	st := e.keyChecks[row.ID]
	if st == nil || st.key != digest {
		st = &keyCheckState{key: digest}
		e.keyChecks[row.ID] = st
	}
	if !st.inflight && (st.at.IsZero() || time.Since(st.at) >= keyCheckEvery) {
		st.inflight = true
		go e.runKeyCheck(k, row.ID, digest)
	}
	return st.changed
}

// runKeyCheck asks the platform once and records the answer against the key it
// was asked about. A failed check keeps the previous answer and is not retried
// before keyCheckEvery: the remedy offered must never flicker with a flaky
// platform API, and a refused destination is still told why it was refused.
func (e *Engine) runKeyCheck(k KeyChecker, destID int64, digest [32]byte) {
	ctx, cancel := context.WithTimeout(context.Background(), keyCheckTimeout)
	defer cancel()
	changed, ok, err := k.StreamKeyChanged(ctx, destID)
	if err != nil {
		e.log.Warn("stream key cross-check failed", "dest", destID, "err", err)
	}
	e.keyMu.Lock()
	st := e.keyChecks[destID]
	if st != nil && st.key == digest {
		st.inflight = false
		st.at = time.Now()
		if err == nil {
			st.changed = ok && changed
		}
	}
	e.keyMu.Unlock()
	if err == nil && ok && changed && e.bus != nil {
		e.publishStatus()
	}
}

// destinationReason is DestStatus.Reason: why this destination is not
// delivering, or is delivering too slowly.
//
// Two sources, and the engine is the right place to combine them because only
// it knows about the source:
//
//   - The supervisor's verdict on the run that failed (Status.Reason). Reported
//     for as long as the process has not moved media again; the supervisor
//     clears it the moment it does.
//   - UPLOAD TOO SLOW WHILE STILL RUNNING. A throttled upload does not exit --
//     the capture in internal/failreason/testdata ran for a minute at 0.015x
//     and printed nothing -- so if this waited for an exit, the cause the
//     operator most needs to hear about would never be named. Judged on the
//     supervisor's WINDOWED rate, never on FFmpeg's speed=, which is averaged
//     over the run. And only once the source has been arriving for a whole
//     window: with the ingest gone every destination's rate falls, and that is
//     one ingest event, not a slow upload per platform. Crawling (0 < rate),
//     not stopped: a stall is destinationStalled's to report.
func destinationReason(p *supervisor.Status, ingestConfigured bool, sourceLive time.Duration, now time.Time) *failreason.Reason {
	if p == nil {
		return nil
	}
	if p.State == supervisor.StateRunning && p.RateKnown && p.Rate > 0 && p.Rate < failreason.SlowRate &&
		(!ingestConfigured || sourceLive >= supervisor.RateWindow) {
		return &failreason.Reason{Cause: failreason.UploadTooSlow, At: now}
	}
	if p.Reason == nil {
		return nil
	}
	r := *p.Reason
	return &r
}

// timelineEventOf is the one mapping from an alert to its timeline row. The
// Subject is the alert's key, which is what lets the Monitoring timeline offer
// the log of the destination or ingest a row is about (A13).
func timelineEventOf(ev alerts.Event) db.TimelineEvent {
	return db.TimelineEvent{
		At: ev.At, Type: string(ev.Type), Severity: string(ev.Severity),
		Title: ev.Title, Detail: ev.Text, Subject: ev.Key,
	}
}
