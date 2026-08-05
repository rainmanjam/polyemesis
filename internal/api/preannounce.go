package api

// Pre-announcing a Facebook broadcast, so a scheduled show has an event page
// before it starts.
//
// WHY IT IS HERE AND NOT IN internal/scheduler
//
// That package opens with a promise: "It does not start or stop anything
// itself. A schedule flips the stored 'enabled' intent through the same path
// the API uses and then asks for a reconcile [...] there is exactly one way a
// destination comes up." A Graph API call inside it would break that. This
// reads schedules through the helpers it already exports and lives where the
// OAuth tokens are.
//
// NOTHING HERE MAY FAIL A SCHEDULE OR A GO-LIVE. It runs ahead of the stream
// and the stream does not depend on it: a Graph error is logged and retried on
// the next sweep, and the destination goes live on time with a broadcast
// created the ordinary way. Every function below returns nothing for that
// reason -- there is no caller that could act on a failure, and inventing one
// would make an optional discovery feature able to stop a broadcast.
//
// WHAT IT WRITES, AND WHAT IT DELIBERATELY DOES NOT. Three columns: the primary
// stream key, the backup endpoint, and the Facebook block that holds the
// announcement markers. Every write goes through db.UpdateAnnouncement, which
// re-reads the row inside its own transaction -- see the comment there for why
// a full-row write from a pre-Graph snapshot is an operator's edits silently
// reverted. And it never touches a destination that is currently ENABLED,
// because the stream key it writes is inside the engine's restart hash.

import (
	"context"
	"errors"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

const (
	// facebookScheduleHorizon is Facebook's own bound on how far ahead a
	// broadcast may be scheduled. Not ours to widen.
	//
	// It constrains far less than it looks like it does: the NEXT occurrence of
	// a daily schedule is at most a day away and of a weekly one at most seven
	// days, by definition. Only a `once` schedule can be set beyond it.
	facebookScheduleHorizon = 7 * 24 * time.Hour

	// preannounceTick is how often the horizon is re-checked.
	//
	// Minutes rather than seconds, and deliberately much slower than the
	// scheduler's own 20-second sweep. That one is fast because it has a grace
	// window to hit; this one is watching for a schedule that is days away, so
	// arriving a few minutes into the window is indistinguishable from arriving
	// at its first instant.
	preannounceTick = 5 * time.Minute
)

// PreannounceLoop runs the sweep until ctx ends. Started beside RefreshLoop.
func (s *Server) PreannounceLoop(ctx context.Context) {
	tick := time.NewTicker(preannounceTick)
	defer tick.Stop()
	// ONE SWEEP BEFORE THE FIRST TICK. A ticker's first fire is a whole period
	// away, so a daemon restarted two minutes before a show would have waited
	// five and announced nothing -- and the loop's behaviour after a restart
	// would differ from its behaviour in steady state, which is the sort of
	// difference nothing tests and everyone assumes away.
	s.preannounceOnce(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.preannounceOnce(ctx, time.Now())
		}
	}
}

// preannounceOnce is one sweep, separated from the loop so a test can drive it
// without waiting on a ticker.
func (s *Server) preannounceOnce(ctx context.Context, now time.Time) {
	scheds, err := s.store.ListSchedules()
	if err != nil {
		s.log.Warn("pre-announce: cannot read schedules", "err", err)
		return
	}
	dests, err := s.store.ListDestinations()
	if err != nil {
		s.log.Warn("pre-announce: cannot read destinations", "err", err)
		return
	}

	for _, sc := range scheds {
		// ActionStart only. A stop schedule has nothing to announce, and
		// creating an event page for one would advertise a show ending.
		if !sc.Enabled || sc.Action != scheduler.ActionStart {
			continue
		}
		at, ok := scheduler.Next(sc, now)
		if !ok || at.Sub(now) > facebookScheduleHorizon {
			continue
		}
		for _, d := range dests {
			if !scheduleTargets(sc, d.ID) || d.Platform != db.PlatformFacebook {
				continue
			}
			// No connected account means no token and no target, so there is
			// nothing to create the broadcast against.
			if d.AccountID == nil {
				continue
			}
			// AN ENABLED DESTINATION IS OUT OF SCOPE, and this is the whole
			// A5 fix rather than half of one.
			//
			// Creating a broadcast issues a NEW primary stream key, and that
			// key is inside Target(), which is the first element of destSpec --
			// the engine's restart hash. Writing it under a running FFmpeg
			// leaves the process publishing to the old key until some unrelated
			// reconcile happens to notice the spec changed, and then cycles a
			// LIVE destination at a moment the operator did not choose and
			// cannot connect to anything they did.
			//
			// Skipping costs the event page for a show whose destination was
			// left enabled between broadcasts; the alternative costs a live
			// stream. The ordinary shape -- the scheduler flips enabled at go
			// live -- is unaffected, because the destination is disabled while
			// the show is still ahead of it.
			if d.Enabled {
				s.log.Debug("pre-announce: skipping a destination that is already enabled; "+
					"a new broadcast would replace the stream key it is publishing to",
					"destination", d.Name, "schedule", sc.Name)
				continue
			}
			if d.Facebook.AnnouncedFor(at) {
				continue
			}
			s.announceOne(ctx, sc, d, at, now)
		}
	}
}

// scheduleTargets reports whether this schedule acts on this destination.
//
// An EMPTY DestinationIDs means EVERY destination -- "start the show" usually
// names nothing, so this is the commonest shape and a rule that skipped it
// would switch the feature off for most installs.
//
// It is also why the marker is keyed by schedule: two schedules of this shape
// both reach every Facebook destination, and a marker that could hold only one
// of them had the two moving one broadcast back and forth forever.
func scheduleTargets(sc scheduler.Schedule, destID int64) bool {
	if len(sc.DestinationIDs) == 0 {
		return true
	}
	for _, id := range sc.DestinationIDs {
		if id == destID {
			return true
		}
	}
	return false
}

// announceOne creates this schedule's broadcast, or moves the one it already
// has.
//
// PER SCHEDULE, not per destination. Two start schedules are two shows, and
// each needs its own event page: the previous design kept one broadcast per
// destination, so the second schedule to run in a sweep rescheduled the first
// one's broadcast to its own occurrence, the next sweep moved it back, and
// subscribers were notified of a time change every five minutes forever.
//
// No failure path leaves a marker that names a broadcast which does not exist:
// that would suppress every later attempt for the occurrence, and the sweep
// would go quiet with nothing saying why. The one thing a failure CAN leave is
// a marker with no broadcast id, which is the opposite claim -- "a create was in
// flight and its outcome is unknown" -- and exists so that a create Facebook
// accepted but this process never recorded is not created a second time.
func (s *Server) announceOne(ctx context.Context, sc scheduler.Schedule, d *db.Destination, at, now time.Time) {
	// tokenFor returns the ACCOUNT with a refreshed token on it, not a token
	// string, and it does the refresh -- which is why it is used here rather
	// than GetPlatformAccount.
	acct, err := s.tokenFor(ctx, *d.AccountID)
	if err != nil {
		s.log.Warn("pre-announce: no usable token", "destination", d.Name, "err", err)
		return
	}

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	prev, held := d.Facebook.AnnouncementFor(sc.ID)
	switch {
	case held && prev.BroadcastID != "":
		// This schedule already has a broadcast, for a DIFFERENT occurrence --
		// the schedule moved. Move the broadcast rather than creating a second
		// one, which would leave the first as an event page people are still
		// subscribed to for a show that will not happen there.
		if err := s.rescheduleFn(cctx, acct, prev.BroadcastID, at); err != nil {
			s.log.Warn("pre-announce: could not move the broadcast",
				"destination", d.Name, "schedule", sc.Name, "err", err)
			s.noteRescheduleFailure(d, sc, prev, now)
			return
		}
		s.clearAnnounceFailures(d.ID, sc.ID)
		s.record(d, func(cur *db.Destination) bool {
			cur.Facebook.Announce(sc.ID, at, prev.BroadcastID, now)
			return true
		})
		s.log.Info("moved a scheduled Facebook broadcast",
			"destination", d.Name, "schedule", sc.Name, "at", at,
			"broadcast", prev.BroadcastID)
		return

	case held:
		// A marker with no broadcast id: a create was in flight and its result
		// never reached the database. Something may well exist on Facebook that
		// nothing here can see, and creating a second live_video would put two
		// event pages in front of the same subscribers. Left for the operator,
		// said once rather than every sweep, and cleared with the occurrence.
		if s.noteAnnounceFailure(d.ID, sc.ID) == 1 {
			s.log.Warn("pre-announce: a broadcast create was recorded as started and "+
				"never finished; not creating a second one for the same show",
				"destination", d.Name, "schedule", sc.Name, "at", prev.Occurrence)
		}
		return
	}

	provider, err := oauth.Get(acct.Platform)
	if err != nil {
		return
	}
	creds, err := s.store.GetPlatformCreds(s.box, acct.Platform)
	if err != nil {
		// Not a failure worth shouting about: a platform with no developer
		// credentials cannot do anything at all, and the operator already knows
		// because nothing else on that platform works either.
		return
	}

	// THE INTENT, BEFORE THE CALL. IngestFor creates a real public live_video,
	// and until its id is stored there is no local record of it -- so a write
	// that fails after the call leaves an event page nothing can find and the
	// next sweep creates another one beside it. Recording the attempt first
	// inverts that: the worst a failed write can now leave is a marker saying a
	// create was started, which the branch above refuses to duplicate.
	//
	// A marker-only write, so it is allowed on a destination that went live
	// since the sweep read it. If the write itself fails, nothing is created --
	// strictly better than creating something nothing will remember.
	if !s.record(d, func(cur *db.Destination) bool {
		cur.Facebook.Announce(sc.ID, at, "", now)
		return true
	}) {
		return
	}

	b, err := s.ingestForFn(cctx, provider, creds.ClientID, acct,
		ingestOptionsFor(d, at))
	if err != nil {
		// The intent goes back: Graph refused, so the next sweep is free to try
		// again -- which is the behaviour this whole file is built on.
		s.record(d, func(cur *db.Destination) bool {
			cur.Facebook.Forget(sc.ID, "")
			return true
		})
		// Logged ONCE PER RUN OF FAILURES, not once per sweep. This is where an
		// ineligible account lands -- Facebook requires 60 days and 100
		// followers to schedule, and refuses otherwise -- and that is a fact
		// about the account rather than a fault in the run: it cannot resolve
		// without the operator, and at a five-minute tick the honest report of
		// it was 288 identical warnings a day. The count clears on the next
		// success, so a failure after a working sweep is reported again.
		if s.noteAnnounceFailure(d.ID, sc.ID) == 1 {
			s.log.Warn("pre-announce: could not create the broadcast; further identical "+
				"failures for this show will not be logged until one succeeds",
				"destination", d.Name, "schedule", sc.Name, "err", err)
		}
		return
	}
	s.clearAnnounceFailures(d.ID, sc.ID)

	if backupURL, _ := firstBackup(b); d.Facebook.BackupIngest && backupURL == "" {
		s.log.Warn("pre-announce: Facebook offered no backup ingest endpoint",
			"destination", d.Name)
	}
	ok := s.record(d, func(cur *db.Destination) bool {
		// THE INVARIANT. The key the pre-created broadcast returned has to be
		// the one the encoder publishes to, or the event page people were
		// notified about stays empty beside a live stream.
		//
		// Which is also why this is the one write that refuses a live
		// destination: applying the key would cycle the running process, and
		// NOT applying it while recording the broadcast would leave the
		// invariant broken with a marker saying everything went fine.
		if cur.Enabled {
			return false
		}
		cur.StreamKey = b.Ingest.Key
		// The same store as handleRefreshKey, for the same reason: this path
		// also creates the broadcast, and a refresh-key-only implementation
		// would lose backup ingest for every pre-announced show.
		cur.BackupURL, cur.BackupStreamKey = firstBackup(b)
		cur.Facebook.Announce(sc.ID, at, b.ID, now)
		return true
	})
	if !ok {
		return
	}
	s.log.Info("pre-announced a Facebook broadcast",
		"destination", d.Name, "schedule", sc.Name, "at", at, "broadcast", b.ID)
}

// staleBroadcastAfter is how many CONSECUTIVE failed reschedules mean the
// broadcast is gone rather than unreachable.
//
// Consecutive, not cumulative -- the same distinction DestResilience.GiveUpAfter
// draws and for the same reason: a destination that fails once an hour for a
// week must never accumulate its way to a verdict.
//
// Three, against a five-minute sweep, is fifteen minutes of consistent refusal.
// A network blip does not last that; a deleted video refuses forever.
const staleBroadcastAfter = 3

// noteRescheduleFailure counts a refusal and, past the threshold, concludes the
// broadcast no longer exists.
//
// WHY COUNTING RATHER THAN READING THE ERROR. Issue #82 stalled on telling
// "deleted" from "network blip", which needs Graph's error codes for a deleted
// LiveVideo -- and Graph documents no update surface for LiveVideo at all, so
// there is nothing authoritative to match against. Guessing a code wrong in one
// direction orphans a live event page; wrong in the other, it creates a
// duplicate one that people are also subscribed to.
//
// Counting needs no such guess. It asks a question the answer to which is
// observable: has this failed EVERY time for long enough that "temporarily
// unreachable" has stopped being a credible explanation.
func (s *Server) noteRescheduleFailure(d *db.Destination, sc scheduler.Schedule,
	prev db.Announcement, now time.Time) {
	if s.noteAnnounceFailure(d.ID, sc.ID) < staleBroadcastAfter {
		return
	}

	// Clear the marker, not the destination. The next sweep sees this schedule
	// holding no broadcast and creates a fresh one, which restores the event
	// page. The stream was never at risk: it publishes to the stored key either
	// way, and a key that has also stopped working is handled by the operator
	// pressing Refresh key.
	s.log.Warn("pre-announce: giving up on a scheduled broadcast that will not move; "+
		"a fresh one will be created",
		"destination", d.Name, "schedule", sc.Name, "broadcast", prev.BroadcastID,
		"consecutiveFailures", staleBroadcastAfter)
	s.record(d, func(cur *db.Destination) bool {
		cur.Facebook.Forget(sc.ID, prev.BroadcastID)
		return true
	})
	s.clearAnnounceFailures(d.ID, sc.ID)
}

// announceFailKey packs the two ids that identify one SHOW into the one int64
// key s.rescheduleFails offers.
//
// Per (destination, schedule) rather than per destination, because a
// destination now holds one broadcast per schedule: with a per-destination
// count, one schedule's create failing three times would trip the give-up on
// ANOTHER schedule's perfectly good broadcast and orphan its event page.
//
// The packing is safe for any id below 2^31, which is every id SQLite will hand
// out in the lifetime of an install. Widening the map's value type to a struct
// would be the honest fix and it is one line in api.go, which this change does
// not own -- recorded here so the next edit there can take it.
func announceFailKey(destID, scheduleID int64) int64 {
	return destID<<32 | (scheduleID & 0xFFFFFFFF)
}

// noteAnnounceFailure counts one consecutive failure for this show and returns
// the running total. One is the first of a run, which is the only one worth
// logging.
func (s *Server) noteAnnounceFailure(destID, scheduleID int64) int {
	s.preannounceMu.Lock()
	defer s.preannounceMu.Unlock()
	if s.rescheduleFails == nil {
		s.rescheduleFails = map[int64]int{}
	}
	k := announceFailKey(destID, scheduleID)
	s.rescheduleFails[k]++
	return s.rescheduleFails[k]
}

// clearAnnounceFailures resets the count. Called on every success, which is
// what makes the threshold mean "consecutive" and what makes a warning that
// stopped and started again get reported.
func (s *Server) clearAnnounceFailures(destID, scheduleID int64) {
	s.preannounceMu.Lock()
	delete(s.rescheduleFails, announceFailKey(destID, scheduleID))
	s.preannounceMu.Unlock()
}

// record applies apply to the destination AS IT STANDS NOW and writes back only
// the columns this sweep owns. On success the caller's copy is refreshed, so
// the next schedule in the same sweep decides against what the last one wrote
// rather than against a snapshot taken before any of it happened.
func (s *Server) record(d *db.Destination, apply func(*db.Destination) bool) bool {
	updated, err := s.store.UpdateAnnouncement(d.ID, apply)
	switch {
	case errors.Is(err, db.ErrAnnouncementSkipped):
		s.log.Warn("pre-announce: the destination went live while its event page was "+
			"being created, so its stream key was left alone; the broadcast may "+
			"need to be removed on Facebook", "destination", d.Name)
		return false
	case err != nil:
		s.log.Warn("pre-announce: could not record the announcement",
			"destination", d.Name, "err", err)
		return false
	}
	*d = *updated
	return true
}
