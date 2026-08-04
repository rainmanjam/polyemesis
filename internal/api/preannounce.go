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

import (
	"context"
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
			if d.Facebook.AnnouncedFor(at) {
				continue
			}
			s.announceOne(ctx, d, at)
		}
	}
}

// scheduleTargets reports whether this schedule acts on this destination.
//
// An EMPTY DestinationIDs means EVERY destination -- "start the show" usually
// names nothing, so this is the commonest shape and a rule that skipped it
// would switch the feature off for most installs.
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

// announceOne creates the broadcast, or moves an existing one.
//
// Every failure path returns WITHOUT touching the destination. A half-written
// marker -- a ScheduledFor recorded for a broadcast that was not created --
// would suppress every later attempt for that occurrence, which is worse than
// having no event page at all: the sweep would go quiet and nothing would say
// why.
func (s *Server) announceOne(ctx context.Context, d *db.Destination, at time.Time) {
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

	// A broadcast already exists for a DIFFERENT occurrence -- the schedule
	// moved. Move the broadcast rather than creating a second one, which would
	// leave the first as an event page people are still subscribed to for a
	// show that will not happen there.
	if d.Facebook.BroadcastID != "" {
		if err := s.rescheduleFn(cctx, acct, d.Facebook.BroadcastID, at); err != nil {
			s.log.Warn("pre-announce: could not move the broadcast",
				"destination", d.Name, "err", err)
			return
		}
		d.Facebook.ScheduledFor = at
		s.saveAnnouncement(d)
		s.log.Info("moved a scheduled Facebook broadcast",
			"destination", d.Name, "at", at, "broadcast", d.Facebook.BroadcastID)
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

	b, err := s.ingestForFn(cctx, provider, creds.ClientID, acct,
		ingestOptionsFor(d, at))
	if err != nil {
		// Logged and dropped. The schedule and the go-live path are unaffected;
		// the next sweep tries again.
		//
		// This is also where an ineligible account lands -- Facebook requires
		// 60 days and 100 followers to schedule, and refuses otherwise. That is
		// a fact about the account rather than a fault in the run, and it will
		// repeat every sweep. See the note in the plan: suppressing a repeated
		// identical refusal needs somewhere to remember it, which is not built.
		s.log.Warn("pre-announce: could not create the broadcast",
			"destination", d.Name, "err", err)
		return
	}

	// THE INVARIANT. The key the pre-created broadcast returned has to be the
	// one the encoder publishes to, or the event page people were notified
	// about stays empty beside a live stream.
	d.StreamKey = b.Ingest.Key
	// The same store as handleRefreshKey, for the same reason: this path also
	// creates the broadcast, and a refresh-key-only implementation would lose
	// backup ingest for every pre-announced show.
	d.BackupURL, d.BackupStreamKey = firstBackup(b)
	if d.Facebook.BackupIngest && d.BackupURL == "" {
		s.log.Warn("pre-announce: Facebook offered no backup ingest endpoint",
			"destination", d.Name)
	}
	d.Facebook.BroadcastID = b.ID
	d.Facebook.ScheduledFor = at
	s.saveAnnouncement(d)
	s.log.Info("pre-announced a Facebook broadcast",
		"destination", d.Name, "at", at, "broadcast", b.ID)
}

func (s *Server) saveAnnouncement(d *db.Destination) {
	if _, err := s.store.UpdateDestination(d); err != nil {
		s.log.Warn("pre-announce: could not record the announcement",
			"destination", d.Name, "err", err)
	}
}
