package api

// YouTube's live-now broadcast, from the coordinator's side.
//
// WHAT CHANGED AGAINST THE HEADER OF lifecycle.go: "It does not create broadcasts" stays true for the sweep's
// decisions and is no longer true of YouTube. A YouTube destination linked to a connected account has its own
// broadcast opened here (oauth.YouTube.OpenLiveNow) when it is on and has none, so the broadcast polyemesis ends,
// renews and reads chat from is its own. THE RULE STILL HOLDS: opening can fail, hold or wait and none of it can
// reach a process, because this file, like lifecycle.go, has no handle on one.
//
// Four things are deliberate:
//
//   - A FAILED OPEN NEVER STOPS THE STREAM. It is a fault on the row (counted, alerted once), a quiet hold, or a
//     pause for quota; the encoder keeps sending to the key it was given.
//   - A STREAM THAT ALREADY HAS A LIVE BROADCAST IS LEFT ALONE, quietly: the row carries the sentence, but no
//     alert or webhook goes out. This is what every YouTube destination that was already on air looks like on the
//     first sweep after an upgrade, and an alert per live destination would be noise. It is held until the
//     destination comes back up or is switched off and on.
//   - A SPENT QUOTA IS A PAUSE, NOT A FAILURE: nothing is retried against an allowance that is gone, and the
//     attempts budget is not burned. Ending is never gated on it.
//   - RENEWAL IS OPEN-FIRST. The finished broadcast's id stays on the row until the next one exists, because
//     ActiveBroadcastID falls back to the announcement mirror for every platform but X, and a wipe-then-open that
//     failed would bring back an announced id. Only a broadcast marked Opened is renewed.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// ytOpenUnits is what one open costs: the stream list, the active read, the upcoming read, the insert and the bind,
// a unit each.
// Using a picked event swaps the insert for a read of it (and sends a bind only when it is not already on the
// stream), so it never costs more. A category adds the category list, the video read and the video update.
const (
	ytOpenUnits     = 5
	ytCategoryUnits = 52
)

// openLock serialises opens of one destination.
func (c *lifecycleCoordinator) openLock(id int64) func() {
	c.openOnce.Do(func() { c.openLocks = newKeyedMutex() })
	return c.openLocks.Lock(id)
}

// ytTitleFor is the broadcast title: the destination's go-live title (or the recurring show's rendered one),
// else the destination's name.
func (c *lifecycleCoordinator) ytTitleFor(d *db.Destination) string {
	if t := strings.TrimSpace(c.titleFor(d)); t != "" {
		return t
	}
	return strings.TrimSpace(d.Name)
}

// renewYouTube opens the next broadcast for a destination whose own has finished.
func (c *lifecycleCoordinator) renewYouTube(ctx context.Context, creator oauth.BroadcastCreator, d *db.Destination) {
	c.tellChat(d.Platform, d.AccountID, "")
	c.log.Info("broadcast lifecycle: the broadcast polyemesis opened has finished and the destination is still on air; "+
		"opening the next one", "destination", d.Name, "platform", d.Platform)
	c.open(ctx, d, creator)
}

// holdQuiet records text on the row without raising it, and (hold) uses up the attempts budget so nothing calls
// again until an UP edge or a switch-off. It writes only when something changes.
func (c *lifecycleCoordinator) holdQuiet(d *db.Destination, broadcastID, text string, hold bool) {
	if d.Lifecycle.Fault == text && (!hold || d.Lifecycle.Attempts >= lifecycleGiveUpAfter) {
		return
	}
	c.recordControl(d, broadcastID, func(b *db.BroadcastControl) {
		b.Fault = text
		if hold && b.Attempts < lifecycleGiveUpAfter {
			b.Attempts = lifecycleGiveUpAfter
		}
	})
}

// openYouTube opens one live-now broadcast for d and records it.
func (c *lifecycleCoordinator) openYouTube(ctx context.Context, d *db.Destination, op oauth.LiveNowOpener) {
	// ONE OPEN AT A TIME PER DESTINATION. The start path opens the broadcast before video is allowed to flow and the
	// sweep opens one for an enabled row that has none; without this both could insert. After the lock the row is
	// read again: if it now names a different broadcast than the one this call was made about, somebody opened it.
	unlock := c.openLock(d.ID)
	defer unlock()
	if cur, err := c.store.GetDestination(d.ID); err == nil && cur != nil {
		if lifecycleBroadcastID(cur) != lifecycleBroadcastID(d) {
			return
		}
		d = cur
	}
	// Failures are recorded against the id the row already holds (the finished broadcast, on a renewal), never
	// against "": recordControl would store "" and the announcement mirror would come back.
	failID := lifecycleBroadcastID(d)
	units := ytOpenUnits
	if y := d.Compliance.YouTube; y != nil && strings.TrimSpace(y.Category) != "" && strings.TrimSpace(y.EventID) == "" {
		units += ytCategoryUnits
	}
	if c.quotaAfford != nil && !c.quotaAfford(units) {
		c.holdQuiet(d, failID, "YouTube's daily quota for this install is spent, so polyemesis has not opened a "+
			"broadcast; it tries again when the allowance resets. The stream is unaffected.", false)
		return
	}
	acct, err := c.tokenFor(ctx, *d.AccountID)
	if err != nil {
		c.noteFailureUnlessPaused(d, failID, scrubSourceKey(fmt.Sprintf(
			"the connected %s account could not be used to open a broadcast: %v", d.Platform, err), d.StreamKey), err)
		return
	}
	req := oauth.LiveNowRequest{
		StreamKey: d.StreamKey,
		Title:     c.ytTitleFor(d),
		Privacy:   string(d.Compliance.Privacy),
	}
	req.MadeForKids = d.Compliance.MadeForKids
	if y := d.Compliance.YouTube; y != nil {
		req.StreamID, req.EventID, req.RebindEvent = y.StreamID, y.EventID, y.RebindEvent
		req.Description, req.Category, req.Projection360 = y.Description, y.Category, y.Projection360
		req.Latency, req.EnableDvr = y.LatencyPreference, y.EnableDvr
	}
	if sendsCaptionPOST(d) {
		req.CaptionPOST, req.CaptionDelay = true, time.Duration(d.DelaySeconds)*time.Second
	}
	// THE ATTEMPT MARKER proves a broadcast is ours, so it is stamped immediately before EACH insert (never by an
	// attempt that stops earlier), is believed for twelve minutes only, and lets the open reuse only a broadcast whose
	// start time is where that insert would have put it. See oauth.LiveNowRequest.AttemptAt.
	if ms := d.Lifecycle.OpenAttemptMS; ms != 0 {
		if at := time.UnixMilli(ms); c.clock().Sub(at) > oauth.LiveNowAttemptMaxAge {
			if d = c.recordControl(d, failID, func(b *db.BroadcastControl) { b.OpenAttemptMS = 0 }); d == nil {
				return
			}
		} else {
			req.AttemptAt = at
		}
	}
	req.BeforeInsert = func() error {
		if c.recordControl(d, failID, func(b *db.BroadcastControl) { b.OpenAttemptMS = c.clock().UnixMilli() }) == nil {
			return errors.New("the lifecycle record could not be written")
		}
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, lifecycleCallTimeout)
	defer cancel()
	res, err := op.OpenLiveNow(cctx, acct.AccessToken, req)
	if err != nil {
		c.openYouTubeFailed(d, failID, err)
		return
	}
	c.forgetTarget(d.ID)
	recorded := c.recordControl(d, res.BroadcastID, func(b *db.BroadcastControl) {
		b.Phase, b.Attempts, b.Fault, b.Opened, b.Created = phaseCreated, 0, "", true, !res.Existing
		b.OpenAttemptMS = 0
	})
	if recorded != nil {
		// Tracked at once, so a destination deleted before the next sweep still has its never-aired broadcast cleaned up.
		c.trackAs(recorded, res.BroadcastID, phaseCreated)
	}
	if recorded == nil {
		// THE RECORD COULD NOT BE WRITTEN (the row was reset, or the store failed), so nothing here would ever find
		// this broadcast again and the next sweep would make another. One polyemesis created is deleted; a picked
		// event is left as it was.
		if !res.Existing {
			if del, ok := op.(oauth.UnairedDeleter); ok {
				dctx, dcancel := context.WithTimeout(ctx, lifecycleCallTimeout)
				defer dcancel()
				if err := del.DeleteUnairedBroadcast(dctx, acct.AccessToken, res.BroadcastID, phaseCreated); err != nil {
					c.log.Warn("broadcast lifecycle: could not record the broadcast it opened, and could not delete it; "+
						"delete it in YouTube Studio", "destination", d.Name, bcAttr(res.BroadcastID),
						"detail", scrubSourceKey(err.Error(), d.StreamKey))
				}
			}
		}
		return
	}
	if res.LiveChatID != "" && c.pointYouTubeChat != nil {
		c.pointYouTubeChat(*d.AccountID, res.LiveChatID)
	}
	for _, w := range res.Warnings {
		c.log.Warn("broadcast lifecycle: a part of the YouTube broadcast did not apply",
			"destination", d.Name, bcAttr(res.BroadcastID), "detail", scrubSourceKey(w, d.StreamKey))
	}
	what := "opened a YouTube broadcast for this destination"
	if res.Existing {
		what = "took over the YouTube event picked for this destination"
	}
	c.log.Info("broadcast lifecycle: "+what+"; it goes live when video arrives",
		"destination", d.Name, "platform", d.Platform, bcAttr(res.BroadcastID))
}

// openYouTubeFailed sorts a failed open into the three ways it can fail.
func (c *lifecycleCoordinator) openYouTubeFailed(d *db.Destination, failID string, err error) {
	var active *oauth.ErrActiveBroadcastOnStream
	switch {
	case errors.As(err, &active):
		why := "this stream already has an active YouTube broadcast (" + active.BroadcastID + "), so polyemesis " +
			"left it alone and made no second one. If it is YouTube Studio's Stream now, turn that off in Studio, " +
			"or switch this destination to use Stream now. It is checked again when the destination is switched off and on."
		c.log.Info("broadcast lifecycle: "+why, "destination", d.Name, "platform", d.Platform)
		c.holdQuiet(d, failID, why, true)
	case oauth.YouTubeQuotaSpent(err):
		if c.quotaPause != nil {
			c.quotaPause()
		}
		c.holdQuiet(d, failID, "YouTube says its daily quota is spent, so polyemesis has not opened a broadcast; it "+
			"tries again when the allowance resets. The stream is unaffected.", false)
	default:
		c.noteFailureUnlessPaused(d, failID, scrubSourceKey(fmt.Sprintf(
			"%s would not open a broadcast for this destination: %v", d.Platform, err), d.StreamKey), err)
	}
}

// deleteIfNeverAired is the end of a destination switched off whose recorded broadcast polyemesis CREATED and that
// is still created or ready: it is deleted (one unit) and the record cleared. A created Public event with auto-start
// on, bound to the destination's stream, would otherwise air on any later video sent to that key. A broadcast that is
// anything else, or was adopted from the operator's own events, is never deleted here. It reports whether it acted.
func (c *lifecycleCoordinator) deleteIfNeverAired(ctx context.Context, prov oauth.BroadcastLifecycler,
	acct *db.PlatformAccount, d *db.Destination, broadcastID, status string) bool {
	if d.Platform != db.PlatformYouTube || !d.Lifecycle.Opened || !d.Lifecycle.Created || d.Lifecycle.BroadcastID != broadcastID {
		return false
	}
	del, ok := prov.(oauth.UnairedDeleter)
	if !ok || !(strings.EqualFold(status, phaseCreated) || strings.EqualFold(status, phaseReady)) {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, lifecycleCallTimeout)
	defer cancel()
	if err := del.DeleteUnairedBroadcast(cctx, acct.AccessToken, broadcastID, status); err != nil {
		c.noteFailureUnlessPaused(d, broadcastID, scrubSourceKey(fmt.Sprintf(
			"could not delete the broadcast polyemesis created and never aired: %v", err), d.StreamKey), err)
		return true
	}
	c.forgetBroadcast(d)
	c.log.Info("broadcast lifecycle: deleted the broadcast polyemesis created, which never aired",
		"destination", d.Name, "platform", d.Platform, bcAttr(broadcastID))
	return true
}

// forgetBroadcast clears the recorded broadcast, releases chat and stops tracking the row.
func (c *lifecycleCoordinator) forgetBroadcast(d *db.Destination) {
	c.recordControl(d, "", func(b *db.BroadcastControl) { *b = db.BroadcastControl{} })
	c.tellChat(d.Platform, d.AccountID, "")
	c.forgetTarget(d.ID)
	c.untrack(d.ID)
}

// releaseYouTube stops driving a broadcast polyemesis opened for a destination that no longer wants one (switched to
// Studio's Stream now). One that never aired and that polyemesis created is deleted; anything else (live, or an
// adopted event) is left exactly as it is and simply forgotten: the operator's Studio runs it from here.
func (c *lifecycleCoordinator) releaseYouTube(ctx context.Context, prov oauth.BroadcastLifecycler, d *db.Destination, broadcastID string) {
	if d.Lifecycle.Created {
		// A created broadcast is forgotten only once its state has been read: a failed token or read keeps the record
		// and the next sweep tries again, because forgetting it unread could strand a never-aired Public broadcast.
		acct, err := c.tokenFor(ctx, *d.AccountID)
		if err != nil {
			c.log.Warn("broadcast lifecycle: could not release the broadcast polyemesis created; will retry",
				"destination", d.Name, bcAttr(broadcastID), "err", scrubSourceKey(err.Error(), d.StreamKey))
			return
		}
		cctx, cancel := context.WithTimeout(ctx, lifecycleCallTimeout)
		st, serr := prov.BroadcastState(cctx, acct.AccessToken, broadcastID)
		cancel()
		if errors.Is(serr, oauth.ErrBroadcastNotListed) {
			// Deleted in Studio (or already by polyemesis): nothing left to release, so stop asking.
			c.forgetBroadcast(d)
			return
		}
		if serr != nil {
			c.log.Warn("broadcast lifecycle: could not read the broadcast polyemesis created to release it; will retry",
				"destination", d.Name, bcAttr(broadcastID), "err", scrubSourceKey(serr.Error(), d.StreamKey))
			return
		}
		if c.deleteIfNeverAired(ctx, prov, acct, d, broadcastID, st.Status) {
			return
		}
	}
	c.forgetBroadcast(d)
	c.log.Info("broadcast lifecycle: this destination now uses YouTube Studio's Stream now, so polyemesis stopped "+
		"driving the broadcast it had opened", "destination", d.Name, bcAttr(broadcastID))
}

// deleteNeverAiredByID reads a broadcast polyemesis created and deletes it if it is still created or ready. It is the
// shared tail of every cleanup that has no row to compare-and-set against (a deleted destination).
func deleteNeverAiredByID(ctx context.Context, prov oauth.BroadcastLifecycler, accessToken, broadcastID string) (bool, error) {
	del, ok := prov.(oauth.UnairedDeleter)
	if !ok {
		return false, nil
	}
	cctx, cancel := context.WithTimeout(ctx, lifecycleCallTimeout)
	defer cancel()
	st, err := prov.BroadcastState(cctx, accessToken, broadcastID)
	if errors.Is(err, oauth.ErrBroadcastNotListed) {
		return false, nil // already gone: nothing to delete, and nothing left to retry
	}
	if err != nil {
		return false, err
	}
	if !(strings.EqualFold(st.Status, phaseCreated) || strings.EqualFold(st.Status, phaseReady)) {
		return false, nil
	}
	return true, del.DeleteUnairedBroadcast(cctx, accessToken, broadcastID, st.Status)
}

// deleteOrphanedNeverAired is endOrphan's first step for a deleted YouTube destination whose recorded broadcast
// polyemesis CREATED and that has not aired: delete it. It reports whether it handled the target (it never handles an
// adopted event, a broadcast that went live, or another platform).
func (c *lifecycleCoordinator) deleteOrphanedNeverAired(ctx context.Context, prov oauth.BroadcastLifecycler, t lifecycleTarget) bool {
	if t.Platform != db.PlatformYouTube || !t.created ||
		!(strings.EqualFold(t.Phase, phaseCreated) || strings.EqualFold(t.Phase, phaseReady)) {
		return false
	}
	if c.orphanAttemptCount(t.destinationID) >= lifecycleGiveUpAfter {
		// The give-up budget applies to this cleanup too (it used to sit below this step and never ran).
		c.untrack(t.destinationID)
		c.log.Warn("broadcast lifecycle: giving up on deleting a deleted destination's never-aired broadcast; delete it "+
			"in YouTube Studio", "destination", t.Name, bcAttr(t.BroadcastID))
		return true
	}
	if _, err := c.store.GetDestination(t.destinationID); !errors.Is(err, db.ErrNotFound) {
		return true // the row is there, or the store cannot say: do nothing now, the next sweep asks again
	}
	acct, err := c.tokenFor(ctx, t.AccountID)
	if err != nil {
		c.noteOrphanFailure(t, err)
		return true
	}
	deleted, err := deleteNeverAiredByID(ctx, prov, acct.AccessToken, t.BroadcastID)
	if err != nil {
		c.noteOrphanFailure(t, err)
		return true
	}
	c.untrack(t.destinationID)
	c.forgetTarget(t.destinationID)
	if deleted {
		c.log.Info("broadcast lifecycle: deleted the never-aired broadcast of a deleted destination",
			"destination", t.Name, bcAttr(t.BroadcastID))
	}
	return true
}

// deleteNeverAiredOnDestinationDelete is the same cleanup made by DELETE /destinations/{id}, which already holds the
// row, so a delete made while the daemon was down is covered too. It returns a sentence for the response's leftovers
// when the broadcast could not be removed.
func (s *Server) deleteNeverAiredOnDestinationDelete(ctx context.Context, d *db.Destination) string {
	if d == nil || d.Platform != db.PlatformYouTube || !d.Lifecycle.Opened || !d.Lifecycle.Created ||
		d.AccountID == nil || d.Lifecycle.BroadcastID == "" ||
		!(strings.EqualFold(d.Lifecycle.Phase, phaseCreated) || strings.EqualFold(d.Lifecycle.Phase, phaseReady)) {
		return ""
	}
	prov, ok := oauth.As[oauth.BroadcastLifecycler](s.providers, d.Platform)
	if !ok {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	acct, err := s.tokenFor(cctx, *d.AccountID)
	if err == nil {
		_, err = deleteNeverAiredByID(cctx, prov, acct.AccessToken, d.Lifecycle.BroadcastID)
	}
	if err == nil && s.lifecycle != nil {
		// Done: the coordinator must not keep reading a broadcast that no longer exists.
		s.lifecycle.untrack(d.ID)
		s.lifecycle.forgetTarget(d.ID)
	}
	if err != nil {
		return "the broadcast polyemesis created for " + d.Name + " (" + d.Lifecycle.BroadcastID +
			") never aired and could not be deleted; delete it in YouTube Studio: " + scrubSourceKey(err.Error(), d.StreamKey)
	}
	return ""
}

// pointChatFromState points the account's YouTube chat adapter at the broadcast's own live chat when a state read of a
// broadcast whose chat is open shows its liveChatId. It covers a broadcast created by polyemesis (whose insert answer
// may carry no id, or whose chat was not open when the open pointed at it) and an adopted event alike, and it is
// repeated on each live read, which is cheap and idempotent and re-points an adapter rebuilt since. Pointing also
// wakes a waiting adapter and clears its "this chat ended" mark. See chat.YouTubeAdapter.SetLiveChatID.
func (c *lifecycleCoordinator) pointChatFromState(d *db.Destination, st *oauth.BroadcastLifecycleState) {
	if c.pointYouTubeChat == nil || d.Platform != db.PlatformYouTube || d.AccountID == nil || st == nil ||
		strings.TrimSpace(st.LiveChatID) == "" {
		return
	}
	switch strings.ToLower(strings.TrimSpace(st.Status)) {
	case phaseLive, phaseTesting, phaseLiveStarting, phaseTestStarting:
		c.pointYouTubeChat(*d.AccountID, st.LiveChatID)
	}
}
