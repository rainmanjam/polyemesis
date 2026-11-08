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
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

const (
	// freshKeyTimeout bounds the Graph round trip a clicked start waits for, the
	// same thirty seconds Refresh stream key allows.
	freshKeyTimeout = 30 * time.Second
	// announcedKeepCap caps how long past its scheduled time an announced
	// event's stored key is kept, whatever the schedule's grace (up to 24h).
	// Nothing records that the announced live video has already aired and been
	// stopped, so a longer window would reuse its dead key on a stop and restart
	// after the show. Fifteen minutes is the time a start for THAT show is
	// plausibly late.
	announcedKeepCap = 15 * time.Minute
	// pendingStartWindow is how long after a renewal a destination that is
	// enabled with no process yet is assumed to be a start in flight (the
	// scheduler writes the flag, and the reconcile that spawns FFmpeg comes
	// after), so a second start in that gap does not mint a second live video.
	pendingStartWindow = 15 * time.Second
)

// scheduledRenewTimeout is the ONE shared deadline for every Facebook renewal of
// a scheduled start. The scheduler's sweep waits for them, and a slow Graph must
// not hold up the YouTube, Twitch and stop schedules in the same sweep. Past it
// a destination starts on the stored key, with a warning in the log. A var so a
// test can shorten it.
var scheduledRenewTimeout = 10 * time.Second

// freshKeyGuard serialises renewals per destination and remembers the last one.
// The zero value is ready to use.
type freshKeyGuard struct {
	once  sync.Once
	locks *keyedMutex

	mu sync.Mutex
	at map[int64]time.Time
}

// lock holds id exclusively until the returned func is called. A start holds it
// from the gate check to the end of its reconcile, so a double-click, a Stream
// Deck press beside the console, or Start all beside a schedule cannot each
// create a live video: the second waits, then finds the process up.
func (g *freshKeyGuard) lock(id int64) func() {
	g.once.Do(func() { g.locks = newKeyedMutex() })
	return g.locks.Lock(id)
}

func (g *freshKeyGuard) note(id int64, at time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.at == nil {
		g.at = map[int64]time.Time{}
	}
	g.at[id] = at
}

func (g *freshKeyGuard) renewedWithin(id int64, now time.Time, d time.Duration) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	t, ok := g.at[id]
	return ok && now.Sub(t) < d
}

// needsFreshFacebookKey reports whether starting d now must first obtain a new
// live video. Pure, so the table of cases is a test and not a read.
//
// inUse is the engine's answer (Manager.DestinationInUse): the destination is
// publishing, respawning, filling or draining a broadcast delay, or retiring.
// It is true for an account-linked Facebook destination that is not in use and
// is not waiting on an announced show. A manual-key Facebook destination (no
// account) is false: there is nothing to renew it with.
//
// An announced event's key is kept while that event's show can still be
// started: until its scheduled time plus the lesser of the announcing
// schedule's grace (graceFor) and announcedKeepCap.
func needsFreshFacebookKey(d *db.Destination, inUse bool, now time.Time,
	graceFor func(scheduleID int64) time.Duration) bool {
	if d == nil || d.Platform != db.PlatformFacebook || d.AccountID == nil || inUse {
		return false
	}
	for _, a := range d.Facebook.Broadcasts() {
		keep := min(graceFor(a.ScheduleID), announcedKeepCap)
		if now.Before(a.Occurrence.Add(keep)) {
			return false
		}
	}
	return true
}

// scheduleGrace is the lateness allowance of the schedule that made an
// announcement; a marker with no schedule, or one whose schedule is gone, gets
// the default.
func (s *Server) scheduleGrace(scheduleID int64) time.Duration {
	if scheduleID != 0 {
		if sc, err := s.store.GetSchedule(scheduleID); err == nil && sc != nil {
			return sc.Grace()
		}
	}
	return time.Duration(scheduler.DefaultGraceSeconds) * time.Second
}

// destinationInUse asks the engines, through a seam a test can replace.
func (s *Server) destinationInUse(id int64) bool {
	if s.destInUse != nil {
		return s.destInUse(id)
	}
	return s.mgr != nil && s.mgr.DestinationInUse(id)
}

// renewFacebookKeyLocked renews the destination's key when a new start needs
// one. THE CALLER HOLDS s.freshKey.lock(id), and the gate is read inside it.
//
// requireEnabled is for Restart, which acts on a destination the operator has
// ON: restarting a disabled one must not create a live video nothing will use.
// A failure never blocks the start (see the package comment); the sentence is
// scrubbed and the key is never in it.
func (s *Server) renewFacebookKeyLocked(ctx context.Context, id int64, requireEnabled bool) (renewed bool, warning string) {
	row, err := s.store.GetDestination(id)
	if err != nil || row == nil {
		return false, "" // the write that follows reports a missing row
	}
	if requireEnabled && !row.Enabled {
		return false, ""
	}
	now := time.Now()
	inUse := s.destinationInUse(id)
	if !needsFreshFacebookKey(row, inUse, now, s.scheduleGrace) {
		return false, ""
	}
	// A start already in flight: enabled, nothing spawned yet, and a key minted
	// moments ago. See pendingStartWindow.
	if row.Enabled && s.freshKey.renewedWithin(id, now, pendingStartWindow) {
		return false, ""
	}
	_, warnings, fail := s.refreshDestinationKey(ctx, id)
	if fail != nil {
		detail := redactErr(fail)
		s.log.Warn("start: could not fetch a fresh Facebook stream key; starting with the stored one",
			"destination", id, "err", detail)
		return false, "polyemesis could not fetch a fresh Facebook stream key (" + detail + "), so the " +
			"destination started with the stored one, which Facebook refuses once its live video has " +
			"ended. Press Refresh stream key to try again."
	}
	s.freshKey.note(id, now)
	if len(warnings) > 0 {
		s.log.Info("start: fetched a fresh Facebook stream key", "destination", id,
			"notes", strings.Join(warnings, " "))
	}
	return true, ""
}

// prepareDestinationStarts is the engine's pre-start hook for SCHEDULED starts
// (Manager.SetStartPrep). It renews every account-linked Facebook target
// concurrently under one shared scheduledRenewTimeout, so one slow Graph answer
// cannot delay the rest of the sweep, and it holds each destination's lock
// until release so a manual start in the gap before the reconcile waits.
func (s *Server) prepareDestinationStarts(ids []int64) (release func()) {
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		held    []func()
		stopped bool
	)
	ctx, cancel := context.WithTimeout(context.Background(), scheduledRenewTimeout)
	for _, id := range ids {
		row, err := s.store.GetDestination(id)
		if err != nil || row == nil || row.Platform != db.PlatformFacebook || row.AccountID == nil {
			continue
		}
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			unlock := s.freshKey.lock(id)
			mu.Lock()
			if stopped { // the sweep gave up waiting; do not hold a lock nobody will free
				mu.Unlock()
				unlock()
				return
			}
			held = append(held, unlock)
			mu.Unlock()
			s.renewFacebookKeyLocked(ctx, id, false)
		}(id)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(scheduledRenewTimeout + time.Second):
	}
	cancel()
	return func() {
		mu.Lock()
		stopped = true
		fns := held
		held = nil
		mu.Unlock()
		for _, f := range fns {
			f()
		}
	}
}
