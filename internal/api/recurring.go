package api

// Recurring broadcast templates (#18): the look-ahead that keeps the next N occurrences of a start
// schedule created as platform events, with each event's title, description, privacy, category and
// thumbnail written from the schedule's template.
//
// WHY IT IS HERE AND NOT IN internal/scheduler. The same reason preannounce.go gives: the scheduler
// promises that it never starts or stops anything itself, and a platform call inside it would break
// that. The template vocabulary (title rendering, the occurrence instants, validation) lives in the
// scheduler package and is pure. The sweep that creates events lives here, where the OAuth tokens are.
//
// WHAT IT CREATES, AND WHAT IT DELIBERATELY DOES NOT. Only a destination whose provider is a
// scheduled editor (oauth.ScheduledEditor) is given template events. Today that is YouTube. A platform
// that can create a scheduled event but cannot title it is left to the legacy pre-announce path, which
// is the only way a placeholder title could reach a viewer, and this sweep refuses that by construction.
//
// THE GO-LIVE BINDING. The lifecycle coordinator reads a destination's announcement mirror to find the
// broadcast a go-live should drive. The mirror is kept pointing at the FIRST planned occurrence that is
// not skipped: that is the broadcast the next start will use. Each sweep re-asserts it, so a skip, a
// restart, or a show that has passed moves it on by itself. An enabled destination is never written
// to, for the reason preannounce gives: a new event can issue a new stream key, and the destination is
// publishing with the old one.
//
// THE RECORD. One row per (schedule, destination, instant) in recurring_occurrences is the record of
// what exists. A row is claimed BEFORE the platform call, so two sweeps cannot both create the event
// for one occurrence. A failed create releases its claim. A skipped occurrence keeps its row, so the
// look-ahead does not create it again.
//
// INTERNAL API FOR THE ROLLOVER LANE (#19). The planned rollover reuses the same pieces, and they are
// deliberately small:
//   - scheduler.BroadcastTemplate and its Validate/RenderTitle: the template and its title.
//   - scheduler.Occurrences: the instants a look-ahead asks for.
//   - Server.createTemplateEvent: create, title, thumbnail and claim one occurrence.
//   - Server.cancelOccurrence: delete one occurrence's event and record it skipped or removed.
// Rollover should call these and not write its own event create.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// errOccurrenceRefused is the operator's removal being refused for a reason the operator can act on: the
// show is still being created, has already started, or its destination is on air. The calendar answers
// it with 409, and the platform is not called.
var errOccurrenceRefused = errors.New("this occurrence cannot be removed")

const (
	// recurringTick is how often the look-ahead is re-checked. Five minutes, the same as the legacy
	// pre-announce sweep, because a show that is days away does not need more.
	recurringTick = 5 * time.Minute
	// claimAbandonAfter is how long a pending claim may stand before it is treated as abandoned. A
	// create is bounded by its own 30-second timeout, so a claim this old had its process die mid-call.
	// The event such a crash may have left is named in the log and is not reaped automatically.
	claimAbandonAfter = 30 * time.Minute
	// occurrenceRetention is how long a row for a PAST occurrence is kept. The platform event is not
	// touched; only the local row is pruned, so the table does not grow for ever.
	occurrenceRetention = 30 * 24 * time.Hour
	// templateCallTimeout bounds one platform write for one occurrence.
	templateCallTimeout = 30 * time.Second
)

// RecurringLoop runs the look-ahead until ctx ends. Started beside PreannounceLoop.
func (s *Server) RecurringLoop(ctx context.Context) {
	tick := time.NewTicker(recurringTick)
	defer tick.Stop()
	// One sweep before the first tick, for the same reason PreannounceLoop does.
	s.recurringOnce(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.recurringOnce(ctx, time.Now())
		}
	}
}

// recurringOnce is one sweep, split from the loop so a test drives it with a clock.
func (s *Server) recurringOnce(ctx context.Context, now time.Time) {
	tpls, err := s.store.ListBroadcastTemplates()
	if err != nil {
		s.log.Warn("recurring: cannot read templates", "err", err)
		return
	}
	scheds, err := s.store.ListSchedules()
	if err != nil {
		s.log.Warn("recurring: cannot read schedules", "err", err)
		return
	}
	dests, err := s.store.ListDestinations()
	if err != nil {
		s.log.Warn("recurring: cannot read destinations", "err", err)
		return
	}
	schedByID := make(map[int64]scheduler.Schedule, len(scheds))
	for _, sc := range scheds {
		schedByID[sc.ID] = sc
	}
	tplByID := make(map[int64]scheduler.BroadcastTemplate, len(tpls))
	for _, t := range tpls {
		tplByID[t.ScheduleID] = t
	}

	s.reconcileOccurrences(ctx, now, schedByID, tplByID)

	for _, tpl := range tpls {
		sc, ok := schedByID[tpl.ScheduleID]
		// A stop schedule has nothing to announce, the same rule the legacy sweep keeps: an event page
		// for a show ending would advertise the ending.
		if !ok || !sc.Enabled || sc.Action != scheduler.ActionStart {
			continue
		}
		planned := scheduler.Occurrences(sc, now, tpl.Lookahead)
		if len(planned) == 0 {
			continue
		}
		for _, d := range dests {
			if !scheduleTargets(sc, d.ID) || d.AccountID == nil || d.Enabled {
				continue
			}
			sb, ok := s.providers.ScheduledBroadcastsFor(d.Platform)
			if !ok {
				continue
			}
			ed, ok := sb.(oauth.ScheduledEditor)
			if !ok {
				continue
			}
			s.lookaheadFor(ctx, ed, tpl, sc, d, planned, now)
		}
	}
}

// lookaheadFor brings one destination's planned occurrences into line with the record. It walks them
// in time order and creates whatever is missing. The go-live mirror is set to the first occurrence
// that is not skipped, and it is re-asserted every sweep so it follows a skip or a passed show.
func (s *Server) lookaheadFor(ctx context.Context, ed oauth.ScheduledEditor, tpl scheduler.BroadcastTemplate,
	sc scheduler.Schedule, d *db.Destination, planned []time.Time, now time.Time) {
	mirrored := false
	for _, at := range planned {
		occ, err := s.store.OccurrenceFor(sc.ID, d.ID, at)
		switch {
		case err == nil && occ.State == scheduler.OccurrenceSkipped:
			// The operator removed this one. It is neither created nor mirrored.
			continue
		case err == nil && occ.State == scheduler.OccurrenceUpcoming:
			if !mirrored {
				s.pointGoLiveAt(d, sc, at, occ.PlatformEventID, nil, now)
				mirrored = true
			}
			continue
		case err == nil:
			// Pending: another sweep is inside its create. Nothing to mirror yet.
			continue
		case !errors.Is(err, db.ErrNotFound):
			s.log.Warn("recurring: cannot read an occurrence", "destination", d.Name, "err", err)
			return
		}

		b, ok := s.createTemplateEvent(ctx, ed, tpl, sc, d, at, now)
		if !ok {
			// The failure is logged where it happened, and the claim is released or the event is
			// cancelled, so the next sweep is free to try. Each occurrence stands alone, so one that
			// failed does not hold back the ones after it. Nothing is mirrored for a failed create.
			continue
		}
		if !mirrored {
			s.pointGoLiveAt(d, sc, at, b.ID, b, now)
			mirrored = true
		}
	}
}

// createTemplateEvent creates ONE occurrence's platform event, titles it from the template, attaches
// the thumbnail, and records it. It returns the broadcast and true when the event stands. On any
// failure it leaves no claim behind and no unnamed event on the platform, and returns false.
//
// Exported to the rest of the package as a method rather than inlined, because the rollover lane
// (#19) creates the same kind of event and must not carry its own copy of the ordering below.
func (s *Server) createTemplateEvent(ctx context.Context, ed oauth.ScheduledEditor, tpl scheduler.BroadcastTemplate,
	sc scheduler.Schedule, d *db.Destination, at, now time.Time) (*oauth.Broadcast, bool) {
	claimID, err := s.store.ClaimOccurrence(scheduler.Occurrence{
		ScheduleID: sc.ID, DestinationID: d.ID, At: at, State: scheduler.OccurrencePending,
	})
	if errors.Is(err, db.ErrOccurrenceClaimed) {
		return nil, false
	}
	if err != nil {
		s.log.Warn("recurring: cannot claim an occurrence", "destination", d.Name, "err", err)
		return nil, false
	}
	release := func() { _ = s.store.DeleteOccurrence(claimID) }

	loc, err := sc.Location()
	if err != nil {
		release()
		return nil, false
	}
	title, err := scheduler.RenderTitle(tpl.TitlePattern, at, loc)
	if err != nil {
		release()
		s.log.Warn("recurring: the title pattern cannot be rendered", "schedule", sc.Name, "err", err)
		return nil, false
	}

	acct, err := s.tokenFor(ctx, *d.AccountID)
	if err != nil {
		release()
		s.log.Warn("recurring: no usable token", "destination", d.Name, "err", err)
		return nil, false
	}
	creds, err := s.store.GetPlatformCreds(s.box, acct.Platform)
	if err != nil {
		release()
		return nil, false
	}

	cctx, cancel := context.WithTimeout(ctx, templateCallTimeout)
	defer cancel()

	b, err := s.ingestFor(cctx, ed, creds.ClientID, acct, s.ingestOptions(d, at))
	if err != nil {
		release()
		// The platform's reason is kept on the template, redacted, so the operator sees a limit in the
		// screen and not only in the log. The log line below is still written once per run of failures.
		_ = s.store.RecordBroadcastTemplateError(sc.ID, alerts.Redact(err.Error()), time.Now())
		if s.noteAnnounceFailure(d.ID, sc.ID) == 1 {
			s.log.Warn("recurring: could not create the event for an occurrence; further identical "+
				"failures for this show will not be logged until one succeeds",
				"destination", d.Name, "schedule", sc.Name, "err", err)
		}
		return nil, false
	}

	warnings, err := ed.EditScheduledBroadcast(cctx, acct.AccessToken, b.ID, oauth.ScheduledEdit{
		Title:       title,
		Description: tpl.Description,
		Privacy:     tpl.Privacy,
		Category:    tpl.Category,
	})
	if err != nil {
		// THE PLACEHOLDER MUST NOT STAND. The platform created the event with a name polyemesis did not
		// choose and refused to give it the template's. Cancelling it is the only way that does not put
		// a public page with the wrong title in front of viewers. If the cancel also fails, the event is
		// recorded so its id is not lost, and it is named in the log for the operator to remove.
		if cerr := ed.CancelScheduledBroadcast(cctx, acct.AccessToken, b.ID); cerr == nil {
			release()
			s.log.Warn("recurring: the platform refused the event's title, so the event was cancelled",
				"destination", d.Name, "schedule", sc.Name, "err", err)
			return nil, false
		}
		_ = s.store.ResolveOccurrence(claimID, b.ID, "")
		s.log.Error("recurring: the platform refused the event's title and the event could not be "+
			"cancelled; delete it in the platform's own studio",
			"destination", d.Name, "schedule", sc.Name, "event", b.ID, "err", err)
		return nil, false
	}
	for _, w := range warnings {
		s.log.Warn("recurring: the platform did not apply part of the event's metadata",
			"destination", d.Name, "schedule", sc.Name, "event", b.ID, "detail", w)
	}

	if data, mime, terr := s.store.BroadcastThumbnail(sc.ID); terr == nil && len(data) > 0 {
		if err := ed.SetScheduledThumbnail(cctx, acct.AccessToken, b.ID, data, mime); err != nil {
			// A thumbnail is not worth cancelling a show over. Logged, and the event stands.
			s.log.Warn("recurring: the event was created but its thumbnail was refused",
				"destination", d.Name, "schedule", sc.Name, "event", b.ID, "err", err)
		}
	}

	if err := s.store.ResolveOccurrence(claimID, b.ID, title); err != nil {
		s.log.Error("recurring: the event was created but its record could not be written; the next "+
			"sweep will create another event for this occurrence unless it is removed",
			"destination", d.Name, "schedule", sc.Name, "event", b.ID, "err", err)
		return nil, false
	}
	s.clearAnnounceFailures(d.ID, sc.ID)
	_ = s.store.ClearBroadcastTemplateError(sc.ID)
	s.log.Info("recurring: created an event for an upcoming occurrence",
		"destination", d.Name, "schedule", sc.Name, "at", at, "event", b.ID)
	return b, true
}

// pointGoLiveAt sets the destination's announcement mirror to one occurrence. When b is non-nil the
// create just happened, and its stream key and backup endpoint are written too, which is the invariant
// the legacy pre-announce keeps: the key the event was created with is the key the encoder publishes to.
//
// The write refuses an enabled destination for the same reason the legacy path does, and a refused
// write is not an error: it means the destination went live, and its own key is the one that matters.
// A mirror that already names this event is left alone, so the re-assertion every sweep writes nothing.
func (s *Server) pointGoLiveAt(d *db.Destination, sc scheduler.Schedule, at time.Time, broadcastID string,
	b *oauth.Broadcast, now time.Time) {
	_ = s.recordErr(d, func(cur *db.Destination) bool {
		if prev, ok := cur.Facebook.AnnouncementFor(sc.ID); ok && b == nil &&
			prev.BroadcastID == broadcastID && prev.Occurrence.Equal(at) {
			return false
		}
		if cur.Enabled && b != nil {
			return false
		}
		if b != nil {
			cur.StreamKey = b.Ingest.Key
			cur.BackupURL, cur.BackupStreamKey = firstBackup(b)
		}
		cur.Facebook.Announce(sc.ID, at, broadcastID, now)
		return true
	})
}

// reconcileOccurrences cancels the platform events of occurrences whose template or schedule is gone,
// releases abandoned claims, and prunes the rows of occurrences long past. An event that cannot be
// cancelled keeps its row, so the next sweep tries again.
func (s *Server) reconcileOccurrences(ctx context.Context, now time.Time,
	schedByID map[int64]scheduler.Schedule, tplByID map[int64]scheduler.BroadcastTemplate) {
	open, err := s.store.ListOpenOccurrences()
	if err != nil {
		s.log.Warn("recurring: cannot read occurrences", "err", err)
		return
	}
	for _, o := range open {
		if o.State == scheduler.OccurrencePending {
			if now.Sub(o.CreatedAt) > claimAbandonAfter {
				s.log.Warn("recurring: released a claim whose create never finished; an event it "+
					"created, if any, is not removed automatically", "schedule", o.ScheduleID,
					"destination", o.DestinationID)
				_ = s.store.DeleteOccurrence(o.ID)
			}
			continue
		}
		_, schedOK := schedByID[o.ScheduleID]
		_, tplOK := tplByID[o.ScheduleID]
		if !schedOK || !tplOK {
			// A show that has started, or whose destination is on air, may be a page that aired or is
			// airing. Only the local row goes; the platform event is left to the operator.
			if !o.At.After(now) || s.destinationDelivering(o.DestinationID) {
				if err := s.store.DeleteOccurrence(o.ID); err != nil {
					s.log.Warn("recurring: could not drop the row of a started show", "schedule", o.ScheduleID, "err", err)
				}
				continue
			}
			if err := s.cancelOccurrence(ctx, o, true); err != nil {
				s.log.Warn("recurring: an orphaned event could not be cancelled; it will be retried",
					"schedule", o.ScheduleID, "event", o.PlatformEventID, "err", err)
			}
			continue
		}
	}
	// Past occurrences: only the local row goes. The platform event is a page that already aired, and
	// removing it is the operator's choice, not a sweep's.
	cutoff := now.Add(-occurrenceRetention)
	past, err := s.store.ListOccurrences(time.Unix(0, 0), cutoff)
	if err != nil {
		return
	}
	for _, o := range past {
		_ = s.store.DeleteOccurrence(o.ID)
	}
}

// skipOccurrence is the operator's "skip this one". It is cancelOccurrence keeping the row, so the
// occurrence is not created again.
func (s *Server) skipOccurrence(ctx context.Context, id int64) error {
	o, err := s.store.GetOccurrence(id)
	if err != nil {
		return err
	}
	switch o.State {
	case scheduler.OccurrenceSkipped:
		return nil
	case scheduler.OccurrencePending:
		return fmt.Errorf("%w: this show's event is still being created; try again in a moment", errOccurrenceRefused)
	}
	// The show has started, or its destination is on air: the event may be airing, so it is not removed
	// from here. The check is against the clock and the live predicate, so a skip never cancels a live page.
	if !o.At.After(time.Now()) {
		return fmt.Errorf("%w: this show started at %s, so its event is past", errOccurrenceRefused,
			o.At.Format(time.RFC3339))
	}
	if s.destinationDelivering(o.DestinationID) {
		return fmt.Errorf("%w: the destination is live, so its event is on air", errOccurrenceRefused)
	}
	return s.cancelOccurrence(ctx, *o, false)
}

// cancelOccurrence deletes one occurrence's platform event, clears the destination's mirror if it
// named that event, and then either keeps the row as skipped or removes it. A destination that no
// longer exists cannot be reached for its account, so the event is named in the error and its row is
// kept: the operator removes the event on the platform, and the row is the only thing that still knows
// it exists.
func (s *Server) cancelOccurrence(ctx context.Context, o scheduler.Occurrence, removeRow bool) error {
	if o.PlatformEventID != "" {
		d, err := s.store.GetDestination(o.DestinationID)
		if err != nil {
			return fmt.Errorf("the destination for this show no longer exists, so its event %s cannot be "+
				"removed from here; delete it in the platform's own studio: %w", o.PlatformEventID, err)
		}
		sb, ok := s.providers.ScheduledBroadcastsFor(d.Platform)
		if !ok {
			return fmt.Errorf("%s has no scheduled-event support, so event %s cannot be removed", d.Platform, o.PlatformEventID)
		}
		ed, ok := sb.(oauth.ScheduledEditor)
		if !ok || d.AccountID == nil {
			return fmt.Errorf("%s cannot cancel scheduled events from here, or the destination has no account", d.Platform)
		}
		acct, err := s.tokenFor(ctx, *d.AccountID)
		if err != nil {
			return err
		}
		cctx, cancel := context.WithTimeout(ctx, templateCallTimeout)
		defer cancel()
		if err := ed.CancelScheduledBroadcast(cctx, acct.AccessToken, o.PlatformEventID); err != nil {
			return err
		}
		// The mirror named this event, so it must not keep pointing at it. Forget removes only the
		// entry for this schedule and this broadcast, and leaves every other show alone.
		_ = s.recordErr(d, func(cur *db.Destination) bool {
			if prev, ok := cur.Facebook.AnnouncementFor(o.ScheduleID); ok && prev.BroadcastID == o.PlatformEventID {
				cur.Facebook.Forget(o.ScheduleID, o.PlatformEventID)
				return true
			}
			return false
		})
	}
	if removeRow {
		return s.store.DeleteOccurrence(o.ID)
	}
	return s.store.SetOccurrenceState(o.ID, scheduler.OccurrenceSkipped)
}
