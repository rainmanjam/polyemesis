package api

// A Facebook stream key is valid for ONE live video. Once the video it belongs
// to has been on air and ended, the key is dead, and a destination linked to a
// connected account that simply stopped and started again would publish to it:
// Facebook refuses the RTMPS open, FFmpeg reports "Input/output error", the
// verdict is key_rejected, and after three quick refusals the supervisor holds
// the destination ("it keeps being refused quickly") -- over a key polyemesis
// could have renewed itself. Found live on staging, v0.16.0: the operator had to
// press Refresh stream key after every stop.
//
// THE DEVICE: a NEW START of an account-linked Facebook destination fetches a
// fresh live video first, through the same code Refresh stream key runs
// (refreshDestinationKey), before the reconcile that spawns FFmpeg. It is done
// here, in the API layer, because the engine and the supervisor have no OAuth
// token and must not grow one; the API already owns every "press start" path
// (applyDestinationEnabled serves POST /start and the bulk start-all).
//
// WHAT IS A NEW START, AND WHAT IS NOT. A supervisor respawn inside one live
// session (a network drop, an FFmpeg crash) never reaches this file: it re-runs
// the same process with the same key, which is exactly right, because a fresh
// key there would create a second live video and split the broadcast. Pressing
// Start on a destination whose process is already up is likewise not a new
// start and refreshes nothing.
//
// WHY NOT "REFRESH WHEN key_rejected IS DETECTED" INSTEAD. The verdict arrives
// after three refused connects, each of which is a failed attempt Facebook sees
// from the page, and it is reached inside the supervisor, a layer below the
// token. It would also fire for a key that is wrong for a reason a fresh one
// does not cure. Renewing at the start is cheaper and moves no machinery.

import (
	"context"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

const (
	// freshKeyTimeout bounds the Graph round trip a start waits for, the same
	// thirty seconds Refresh stream key allows.
	freshKeyTimeout = 30 * time.Second
	// announcedShowGrace is how long past its scheduled time an announced
	// Facebook event is still the show a manual start belongs to. Within it,
	// the destination's stored key IS that event's key, and replacing it would
	// orphan the public event page the pre-announce sweep created.
	announcedShowGrace = 2 * time.Hour
)

// needsFreshFacebookKey reports whether starting d now must first obtain a new
// live video. Pure, so the table of cases is a test and not a read.
//
// It is true for an account-linked Facebook destination that is not currently
// publishing and is not waiting on an announced show. A manual-key Facebook
// destination (no account) is false: there is nothing to renew it with.
func needsFreshFacebookKey(d *db.Destination, statuses []engine.DestStatus, now time.Time) bool {
	if d == nil || d.Platform != db.PlatformFacebook || d.AccountID == nil {
		return false
	}
	for _, a := range d.Facebook.Broadcasts() {
		if a.Occurrence.After(now.Add(-announcedShowGrace)) {
			return false
		}
	}
	for _, st := range statuses {
		if st.ID != d.ID || !st.Enabled || st.Process == nil {
			continue
		}
		switch st.Process.State {
		case supervisor.StateRunning, supervisor.StateStarting, supervisor.StateReconnecting:
			// Already up (or mid-respawn): the same live video carries on.
			return false
		}
	}
	return true
}

// renewFacebookKeyForStart is called before a destination is switched on. It
// reports whether a fresh key was stored, and returns a sentence for the
// operator when the renewal was needed and failed. A failure never blocks the start: the stored key may still
// be good (a scheduled event nobody announced to us), and refusing to start
// over an optional step would turn a Graph outage into an outage of the show.
// The error text is scrubbed, and the key is never in it.
func (s *Server) renewFacebookKeyForStart(id int64) (renewed bool, warning string) {
	row, err := s.store.GetDestination(id)
	if err != nil || row == nil {
		return false, "" // the store write that follows reports a missing row
	}
	var statuses []engine.DestStatus
	if s.mgr != nil {
		statuses = s.mgr.DestinationStatuses()
	}
	if !needsFreshFacebookKey(row, statuses, time.Now()) {
		return false, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), freshKeyTimeout)
	defer cancel()
	_, warnings, fail := s.refreshDestinationKey(ctx, id)
	if fail != nil {
		detail := redactErr(fail)
		s.log.Warn("start: could not fetch a fresh Facebook stream key; starting with the stored one",
			"destination", id, "err", detail)
		return false, "polyemesis could not fetch a fresh Facebook stream key (" + detail + "), so the " +
			"destination started with the stored one, which Facebook refuses once its live video has " +
			"ended. Press Refresh stream key to try again."
	}
	if len(warnings) > 0 {
		s.log.Info("start: fetched a fresh Facebook stream key", "destination", id,
			"notes", strings.Join(warnings, " "))
	}
	return true, ""
}
