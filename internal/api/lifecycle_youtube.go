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

// ytOpenUnits is what one open costs: the stream list, the active read, the insert and the bind, a unit each.
// Using a picked event swaps the insert for a read of it (and sends a bind only when it is not already on the
// stream), so it never costs more. A category adds the category list, the video read and the video update.
const (
	ytOpenUnits     = 4
	ytCategoryUnits = 52
)

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
	cctx, cancel := context.WithTimeout(ctx, lifecycleCallTimeout)
	defer cancel()
	res, err := op.OpenLiveNow(cctx, acct.AccessToken, req)
	if err != nil {
		c.openYouTubeFailed(d, failID, err)
		return
	}
	c.forgetTarget(d.ID)
	c.recordControl(d, res.BroadcastID, func(b *db.BroadcastControl) {
		b.Phase, b.Attempts, b.Fault, b.Opened = phaseCreated, 0, "", true
	})
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
