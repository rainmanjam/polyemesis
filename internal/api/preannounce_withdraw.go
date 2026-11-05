package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// withdrawStaleAnnouncements removes the pre-announced events of shows that no
// schedule will air any more: the schedule was deleted, switched off, turned
// into a stop, or no longer names the destination.
//
// THE LEGACY PATH NEVER DID THIS. A schedule without a recurring template is
// announced by preannounceOnce, which only ever creates and moves broadcasts
// for schedules that still want them; it never looked at a marker whose
// schedule had gone. So deleting, disabling or retargeting such a schedule left
// the event page up on the platform, announcing a show that would not happen,
// and left its marker on the destination -- where AnnouncedFor, which matches
// a marker of ANY schedule at the same instant, then suppressed the event page
// of a replacement schedule set for the same time.
//
// What it does per marker, mirroring reconcileOccurrences' withdraw:
//
//   - YouTube (a platform that can cancel a scheduled event) is asked to
//     cancel it, and the marker is forgotten once it has, or once the platform
//     says it may not (the show has started).
//   - Facebook offers no call to remove a scheduled live video, so the marker
//     is forgotten and the event is named, here and in the log, for the
//     operator to remove on Facebook.
//
// Not touched: a marker with no schedule id (an older row's single marker,
// which the next announcing schedule adopts), a past occurrence (a show that
// aired), an event a recurring-template occurrence owns (that sweep withdraws
// its own), and a destination on air (the live broadcast may be this event).
//
// only, when non-zero, limits the pass to one schedule's markers, so a
// schedule's own delete or edit can report what it left behind.
func (s *Server) withdrawStaleAnnouncements(ctx context.Context, now time.Time, only int64) []string {
	scheds, err := s.store.ListSchedules()
	if err != nil {
		s.log.Warn("pre-announce: cannot read schedules to withdraw stale events", "err", err)
		return nil
	}
	dests, err := s.store.ListDestinations()
	if err != nil {
		s.log.Warn("pre-announce: cannot read destinations to withdraw stale events", "err", err)
		return nil
	}
	open, err := s.store.ListOpenOccurrences()
	if err != nil {
		s.log.Warn("pre-announce: cannot read occurrences to withdraw stale events", "err", err)
		return nil
	}
	byID := make(map[int64]scheduler.Schedule, len(scheds))
	for _, sc := range scheds {
		byID[sc.ID] = sc
	}
	owned := make(map[string]bool, len(open))
	for _, o := range open {
		if o.PlatformEventID != "" {
			owned[o.PlatformEventID] = true
		}
	}

	var warnings []string
	for _, d := range dests {
		for _, a := range d.Facebook.Broadcasts() {
			if a.ScheduleID == 0 || (only != 0 && a.ScheduleID != only) {
				continue
			}
			if !a.Occurrence.After(now) || owned[a.BroadcastID] {
				continue
			}
			if sc, ok := byID[a.ScheduleID]; ok && sc.Enabled && sc.Action == scheduler.ActionStart &&
				scheduleTargets(sc, d.ID) {
				continue
			}
			if s.destinationLive(d.ID) {
				continue
			}
			if w := s.withdrawAnnouncement(ctx, d, a); w != "" {
				warnings = append(warnings, w)
			}
		}
	}
	return warnings
}

// withdrawAnnouncement removes one stale marker, cancelling its event where the
// platform allows it. It returns what the operator must do by hand, if anything.
func (s *Server) withdrawAnnouncement(ctx context.Context, d *db.Destination, a db.Announcement) string {
	forget := func() {
		_ = s.recordErr(d, func(cur *db.Destination) bool {
			cur.Facebook.Forget(a.ScheduleID, a.BroadcastID)
			return true
		})
	}
	err := s.cancelMarker(ctx, d, a.BroadcastID)
	switch {
	case err == nil:
		forget()
		s.log.Info("pre-announce: cancelled the event of a show no schedule will air",
			"destination", d.Name, "schedule", a.ScheduleID, "event", a.BroadcastID)
		return ""
	case errors.Is(err, errNoCancelCall), errors.Is(err, oauth.ErrScheduledNotCancellable):
		// Nothing from here can remove it, now or later: forget it, so it stops
		// suppressing a replacement show, and name it once.
		forget()
		s.log.Warn("pre-announce: the event of a show no schedule will air was left on the platform; "+
			"remove it there", "destination", d.Name, "schedule", a.ScheduleID, "event", a.BroadcastID,
			"err", redactErr(err))
		return eventLeftWarning(d, a.BroadcastID, fmt.Sprintf("no schedule will air the show on %s",
			a.Occurrence.UTC().Format(time.RFC3339)))
	default:
		// Transient: the marker stays, and the next sweep tries again.
		s.log.Warn("pre-announce: could not cancel the event of a show no schedule will air; it will be retried",
			"destination", d.Name, "schedule", a.ScheduleID, "event", a.BroadcastID, "err", redactErr(err))
		return fmt.Sprintf("%q: the scheduled event %s on %s could not be cancelled yet (%s); it is retried "+
			"every few minutes.", d.Name, a.BroadcastID, platformTitle(d.Platform), redactErr(err))
	}
}
