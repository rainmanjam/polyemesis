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
	"strings"
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

// RecurringLoop runs the look-ahead until ctx ends, and the go-live title write for Twitch and Kick
// (recurring_golive.go) on its own, faster tick. Started beside PreannounceLoop.
func (s *Server) RecurringLoop(ctx context.Context) {
	tick := time.NewTicker(recurringTick)
	defer tick.Stop()
	golive := time.NewTicker(goLiveTick)
	defer golive.Stop()
	// One sweep before the first tick, for the same reason PreannounceLoop does.
	s.recurringOnce(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.recurringOnce(ctx, time.Now())
		case <-golive.C:
			s.goLiveTemplatesOnce(ctx, time.Now())
		}
	}
}

// recurringOnce is one sweep, split from the loop so a test drives it with a clock.
func (s *Server) recurringOnce(ctx context.Context, now time.Time) {
	tpls, err := s.store.ListBroadcastTemplates()
	if err != nil {
		s.log.Warn("recurring: cannot read templates", "err", redactErr(err))
		return
	}
	scheds, err := s.store.ListSchedules()
	if err != nil {
		s.log.Warn("recurring: cannot read schedules", "err", redactErr(err))
		return
	}
	dests, err := s.store.ListDestinations()
	if err != nil {
		s.log.Warn("recurring: cannot read destinations", "err", redactErr(err))
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
			s.lookaheadFor(ctx, ed, tpl, sc, d, now)
		}
	}
}

// lookaheadWalkLimit bounds how many instants one destination's look-ahead walks while it looks for
// N that are not skipped. Four times the largest look-ahead: a run of skips longer than that is an
// operator who removed the whole window, and the walk stops there rather than running on.
const lookaheadWalkLimit = 4 * scheduler.MaxLookahead

// lookaheadFor brings one destination's next N occurrences into line with the record. A skipped
// occurrence does not count toward N: the walk passes over it and the occurrence after the window is
// the one that fills the slot, so the operator always has N shows created ahead of time. A skipped
// occurrence is never created again. The go-live mirror is set to the first counted occurrence, and
// it is re-asserted every sweep so it follows a skip or a passed show.
func (s *Server) lookaheadFor(ctx context.Context, ed oauth.ScheduledEditor, tpl scheduler.BroadcastTemplate,
	sc scheduler.Schedule, d *db.Destination, now time.Time) {
	sc = sc.Normalized()
	if _, err := sc.Location(); err != nil {
		return
	}
	mirrored := false
	counted := 0
	cursor := now
	for step := 0; counted < tpl.Lookahead && step < lookaheadWalkLimit; step++ {
		at, ok := scheduler.Next(sc, cursor)
		if !ok {
			break
		}
		cursor = at
		occ, err := s.store.OccurrenceFor(sc.ID, d.ID, at)
		if err == nil && occ.State == scheduler.OccurrenceSkipped {
			// The operator removed this one. It is neither created nor mirrored, and it does not use a
			// slot, so the walk goes on to the next instant to fill it.
			continue
		}
		counted++
		switch {
		case err == nil && occ.State == scheduler.OccurrenceUpcoming:
			if !mirrored {
				s.pointGoLiveAt(d, sc, at, occ.PlatformEventID, nil, now)
				mirrored = true
			}
			continue
		case err == nil:
			// Pending or stranded: another sweep is inside its create, or the cancel is being retried.
			// It holds its slot and nothing is mirrored for it.
			continue
		case !errors.Is(err, db.ErrNotFound):
			s.log.Warn("recurring: cannot read an occurrence", "destination", d.Name, "err", redactErr(err))
			return
		}

		if s.templateCreateHeld(d.ID, sc.ID, now) {
			// A recent refusal for this destination and schedule. The slot is counted, so the walk does
			// not reach further ahead to fill it, and nothing calls the platform until the hold ends.
			continue
		}
		b, ok := s.createTemplateEvent(ctx, ed, tpl, sc, d, at, now)
		if ok && strings.TrimSpace(d.StreamKey) == "" && b.Ingest.Key != "" {
			// The first create in this walk minted the destination's stream. The next one must be handed the same
			// key as HeldKey, or a destination with an ingest of its own gets a SECOND stream for occurrence two,
			// and the mirror later moves to an event whose key the destination does not hold. A copy, so the
			// caller's row is not changed under it.
			cp := *d
			cp.StreamKey = b.Ingest.Key
			d = &cp
		}
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
		s.log.Warn("recurring: cannot claim an occurrence", "destination", d.Name, "err", redactErr(err))
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
		s.log.Warn("recurring: the title pattern cannot be rendered", "schedule", sc.Name, "err", redactErr(err))
		return nil, false
	}

	acct, err := s.tokenFor(ctx, *d.AccountID)
	if err != nil {
		release()
		s.log.Warn("recurring: no usable token", "destination", d.Name, "err", redactErr(err))
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
		n := s.noteAnnounceFailure(d.ID, sc.ID)
		s.holdTemplateCreate(d.ID, sc.ID, now.Add(templateCreateBackoff(n, err)))
		if n == 1 {
			s.log.Warn("recurring: could not create the event for an occurrence; further identical "+
				"failures for this show will not be logged until one succeeds",
				"destination", d.Name, "schedule", sc.Name, "err", redactErr(err))
		}
		return nil, false
	}

	// Bound to the key the create returned, so it is harmless if that key is never applied.
	if err := s.keepCaptionIngest(d, b); err != nil {
		s.log.Warn("recurring: the caption ingestion URL could not be stored",
			"destination", d.Name, "err", redactErr(err))
	}

	warnings, err := ed.EditScheduledBroadcast(cctx, acct.AccessToken, b.ID, oauth.ScheduledEdit{
		Title:       title,
		Description: tpl.Description,
		Privacy:     templatePrivacy(tpl, d),
		Category:    tpl.Category,
	})
	if err != nil {
		// THE PLACEHOLDER MUST NOT STAND. The platform created the event with a name polyemesis did not
		// choose and refused to give it the template's. Cancelling it is the only way that does not put
		// a public page with the wrong title in front of viewers. If the cancel also fails, the event is
		// recorded so its id is not lost, and it is named in the log for the operator to remove.
		if cerr := s.cancelCreatedEvent(ctx, ed, acct.AccessToken, b.ID); cerr == nil {
			release()
			s.log.Warn("recurring: the platform refused the event's title, so the event was cancelled",
				"destination", d.Name, "schedule", sc.Name, "err", redactErr(err))
			return nil, false
		}
		// NOT ResolveOccurrence: that would record an upcoming show with an empty title. The row is
		// stranded instead, which keeps the event id so reconcileOccurrences retries the cancel every
		// sweep, and which keeps the occurrence out of the list of shows and out of the look-ahead count.
		if serr := s.store.StrandOccurrence(claimID, b.ID); serr != nil {
			s.log.Error("recurring: the event could not be cancelled and its record could not be "+
				"stranded; delete it in the platform's own studio",
				"destination", d.Name, "schedule", sc.Name, "event", b.ID, "err", redactErr(serr))
		}
		s.log.Error("recurring: the platform refused the event's title and the event could not be "+
			"cancelled; the cancel is retried every sweep until it succeeds",
			"destination", d.Name, "schedule", sc.Name, "event", b.ID, "err", redactErr(err))
		return nil, false
	}
	for _, w := range warnings {
		s.log.Warn("recurring: the platform did not apply part of the event's metadata",
			"destination", d.Name, "schedule", sc.Name, "event", b.ID, "detail", alerts.Redact(w))
	}

	if data, mime, terr := s.store.BroadcastThumbnail(sc.ID); terr == nil && len(data) > 0 {
		if err := ed.SetScheduledThumbnail(cctx, acct.AccessToken, b.ID, data, mime); err != nil {
			// A thumbnail is not worth cancelling a show over. Logged, and the event stands.
			s.log.Warn("recurring: the event was created but its thumbnail was refused",
				"destination", d.Name, "schedule", sc.Name, "event", b.ID, "err", redactErr(err))
		}
	}

	if err := s.store.ResolveOccurrence(claimID, b.ID, title); err != nil {
		// A pending claim left here ages out after claimAbandonAfter and the occurrence is created AGAIN, a
		// duplicate public event. So the row is stranded with the event id instead, which the sweep cancels. If
		// even that write fails, the row is gone, and the event is cancelled now so it is not left unnamed.
		if serr := s.store.StrandOccurrence(claimID, b.ID); serr == nil {
			s.log.Error("recurring: the event was created but its record could not be written; it is "+
				"recorded as stranded and will be cancelled", "destination", d.Name, "schedule", sc.Name,
				"event", b.ID, "err", redactErr(err))
			return nil, false
		}
		if cerr := s.cancelCreatedEvent(ctx, ed, acct.AccessToken, b.ID); cerr != nil {
			s.log.Error("recurring: the event was created, its record could not be written and it could not be "+
				"cancelled; delete it in the platform's own studio", "destination", d.Name, "schedule", sc.Name,
				"event", b.ID, "err", redactErr(cerr))
			return nil, false
		}
		s.log.Error("recurring: the event was created but its record could not be written, so it was cancelled",
			"destination", d.Name, "schedule", sc.Name, "event", b.ID, "err", redactErr(err))
		return nil, false
	}
	s.clearAnnounceFailures(d.ID, sc.ID)
	s.holdTemplateCreate(d.ID, sc.ID, time.Time{})
	_ = s.store.ClearBroadcastTemplateError(sc.ID)
	s.log.Info("recurring: created an event for an upcoming occurrence",
		"destination", d.Name, "schedule", sc.Name, "at", at, "event", b.ID)
	return b, true
}

// templateCreateBackoffMax caps the wait between creates after refusals, and is the whole wait when YouTube says
// the channel is at its cap on upcoming broadcasts, which no retry lifts.
const templateCreateBackoffMax = 6 * time.Hour

// templateCreateBackoff is how long a destination's template creates wait after the n-th consecutive refusal:
// one tick, doubling, up to templateCreateBackoffMax. Before this a refusal was retried on every five-minute tick,
// 288 times a day per occurrence, at about 50 quota units each, and a destination with an ingest of its own gained
// another unused stream on every try.
func templateCreateBackoff(n int, err error) time.Duration {
	if errors.Is(err, oauth.ErrUpcomingBroadcastLimit) {
		return templateCreateBackoffMax
	}
	wait := recurringTick
	for i := 1; i < n && wait < templateCreateBackoffMax; i++ {
		wait *= 2
	}
	return min(wait, templateCreateBackoffMax)
}

// holdTemplateCreate sets when the next create for this destination and schedule may be tried. The zero time
// clears it.
func (s *Server) holdTemplateCreate(destID, scheduleID int64, until time.Time) {
	s.preannounceMu.Lock()
	defer s.preannounceMu.Unlock()
	k := announceFailKey(destID, scheduleID)
	if until.IsZero() {
		delete(s.templateCreateHolds, k)
		return
	}
	if s.templateCreateHolds == nil {
		s.templateCreateHolds = map[showKey]time.Time{}
	}
	s.templateCreateHolds[k] = until
}

// templateCreateHeld reports whether a create for this destination and schedule must wait.
func (s *Server) templateCreateHeld(destID, scheduleID int64, now time.Time) bool {
	s.preannounceMu.Lock()
	defer s.preannounceMu.Unlock()
	until, ok := s.templateCreateHolds[announceFailKey(destID, scheduleID)]
	return ok && now.Before(until)
}

// templatePrivacy is the privacy an occurrence's event is written with: the STRICTER of the template's and the
// destination's own compliance privacy (private, then unlisted, then public).
//
// Why not the template's alone. A template saved without a privacy is stored as "public" (scheduler.BroadcastTemplate
// normalises it), so "the operator did not choose" and "the operator chose public" look the same here. A destination
// whose compliance says private or unlisted is a deliberate setting about who may see that channel's shows, and a
// template that merely did not mention privacy must not open its look-ahead events to everyone for days. To publish
// such a destination's shows, change the destination's privacy. A compliance privacy of "unchanged" says nothing,
// and the template's stands.
func templatePrivacy(tpl scheduler.BroadcastTemplate, d *db.Destination) string {
	rank := map[string]int{"public": 1, "unlisted": 2, "private": 3}
	t := strings.ToLower(strings.TrimSpace(tpl.Privacy))
	c := strings.ToLower(strings.TrimSpace(string(d.Compliance.Privacy)))
	if rank[c] > rank[t] {
		return c
	}
	return t
}

// cancelCreatedEvent removes an event this sweep just created, on a context of its own. The create's context may be
// the one that ran out, and reusing it would make the rollback fail for the reason the create did.
func (s *Server) cancelCreatedEvent(ctx context.Context, ed oauth.ScheduledEditor, token, id string) error {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), templateCallTimeout)
	defer cancel()
	return ed.CancelScheduledBroadcast(cctx, token, id)
}

// redactErr is the form every error takes on its way into a log line here. Platform errors carry response bodies,
// and a body can carry a stream URL with its key.
func redactErr(err error) string {
	if err == nil {
		return ""
	}
	return alerts.Redact(err.Error())
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
		s.log.Warn("recurring: cannot read occurrences", "err", redactErr(err))
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
		if o.State == scheduler.OccurrenceStranded {
			// A stranded row keeps its own bounded retry: releaseOpenOccurrence holds it while the destination is live.
			s.releaseOpenOccurrence(ctx, o, now)
			continue
		}
		if !schedOK || !tplOK {
			if err := s.cancelOccurrence(ctx, o, true, now); err != nil {
				if errors.Is(err, errDestinationDelivering) {
					// The destination is live, so the event may be bound to its broadcast. The row is kept and the
					// next pass tries again, which cancels the event once the destination stops.
					s.log.Warn("recurring: an orphaned event's destination is live, so the event is kept and will be "+
						"retried after the destination stops", "schedule", o.ScheduleID, "event", o.PlatformEventID)
					continue
				}
				if errors.Is(err, oauth.ErrScheduledNotCancellable) {
					// The platform will not delete this event: it is live, testing or aired. The row is
					// dropped so the sweep does not come back to it, and the event is named for the operator.
					s.log.Warn("recurring: an orphaned event may not be deleted from here (its show has started, its "+
						"destination is live, or the platform refused), so it was left on the platform; "+
						"remove it in the platform's own studio if it is not wanted",
						"schedule", o.ScheduleID, "event", o.PlatformEventID, "err", redactErr(err))
					_ = s.store.DeleteOccurrence(o.ID)
					continue
				}
				s.log.Warn("recurring: an orphaned event could not be cancelled; it will be retried",
					"schedule", o.ScheduleID, "event", o.PlatformEventID, "err", redactErr(err))
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
	// The go-live claims of Twitch and Kick shows age out on the same clock, so the table stays bounded.
	if err := s.store.PruneGoLiveBefore(cutoff); err != nil {
		s.log.Warn("recurring: cannot prune go-live records", "err", redactErr(err))
	}
}

// releaseOpenOccurrence retries a stranded cancel. A stranded event never went live, so its cancel is retried
// whenever its destination is off air, and dropped once its show has passed. Only stranded rows reach it: an
// orphaned upcoming row (its schedule or template gone) is reconcileOccurrences' own branch, which owns the
// started-show and live-destination rules for those.
func (s *Server) releaseOpenOccurrence(ctx context.Context, o scheduler.Occurrence, now time.Time) {
	if d, err := s.store.GetDestination(o.DestinationID); err == nil && d.Enabled {
		return
	}
	err := s.cancelOccurrence(ctx, o, true, now)
	if err == nil {
		return
	}
	// The retry is bounded by the show's own time. Until the show passes, a refused cancel is retried (a
	// transient failure clears, and a show the operator started is refused every pass). Once it has passed,
	// the record is dropped and the event is named for removal in the platform's studio: a refusal that never
	// clears must not hold the row for ever.
	if !o.At.After(now) {
		if derr := s.store.DeleteOccurrence(o.ID); derr != nil {
			s.log.Warn("recurring: cannot drop a stranded record whose show has passed",
				"schedule", o.ScheduleID, "event", o.PlatformEventID, "err", redactErr(derr))
		}
		s.log.Error("recurring: a stranded event's show has passed and its cancel is still refused; "+
			"its record is dropped, and the event must be removed in the platform's own studio",
			"schedule", o.ScheduleID, "event", o.PlatformEventID, "err", redactErr(err))
		return
	}
	s.log.Warn("recurring: a stranded event could not be cancelled; it will be retried",
		"schedule", o.ScheduleID, "event", o.PlatformEventID, "err", redactErr(err))
}

// skipOccurrence is the operator's "skip this one". It is cancelOccurrence keeping the row, so the
// occurrence is not created again. Every refusal wraps errOccurrenceRefused, which the calendar answers
// as 409, and nothing is cancelled: a started show is the platform's record of it, and a destination that
// is on air (delivering, or enabled and starting) may already be bound to the event.
func (s *Server) skipOccurrence(ctx context.Context, id int64, now time.Time) error {
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
	if !o.At.After(now) {
		return fmt.Errorf("%w: this show started at %s, so its event is past", errOccurrenceRefused,
			o.At.Format(time.RFC3339))
	}
	delivering := s.destinationLive(o.DestinationID)
	if d, derr := s.store.GetDestination(o.DestinationID); derr == nil && d.Enabled {
		delivering = true
	}
	if delivering {
		return fmt.Errorf("%w: the destination is live, so its event is on air", errOccurrenceRefused)
	}
	return s.cancelOccurrence(ctx, *o, false, now)
}

// occurrenceCancelRefusal says why an occurrence's platform event must not be deleted, or returns nil when
// it may be. The start must still be in the future: a show that has begun is the platform's record of it.
// The destination must not be delivering: a live destination may be bound to that very event, and deleting
// it would take the live broadcast down. Both refusals wrap oauth.ErrScheduledNotCancellable, so the sweep
// drops the row and the skip answers 409.
func occurrenceCancelRefusal(o scheduler.Occurrence, now time.Time, delivering bool) error {
	if !o.At.After(now) {
		return fmt.Errorf("%w: its start time %s has passed", oauth.ErrScheduledNotCancellable,
			o.At.UTC().Format(time.RFC3339))
	}
	if delivering {
		return fmt.Errorf("%w: %w", oauth.ErrScheduledNotCancellable, errDestinationDelivering)
	}
	return nil
}

// errDestinationDelivering is the reason an event is refused while its destination is live. Unlike a passed
// start, it is temporary: the event is kept, so the sweep cancels it once the destination stops.
var errDestinationDelivering = errors.New("its destination is delivering, so the event may be bound to the live broadcast")

// destinationLive is the delivering predicate the occurrence paths read. Tests replace it through deliveringFn.
func (s *Server) destinationLive(destinationID int64) bool {
	if s.deliveringFn != nil {
		return s.deliveringFn(destinationID)
	}
	return s.destinationDelivering(destinationID)
}

// cancelOccurrence deletes one occurrence's platform event, clears the destination's mirror if it
// named that event, and then either keeps the row as skipped or removes it. A destination that no
// longer exists cannot be reached for its account, so the event is named in the error and its row is
// kept: the operator removes the event on the platform, and the row is the only thing that still knows
// it exists.
func (s *Server) cancelOccurrence(ctx context.Context, o scheduler.Occurrence, removeRow bool, now time.Time) error {
	// A show that has already started or aired, or whose destination is live, is not deleted from here: the
	// platform page is a record of it, and removing it is the operator's choice on the platform. Only the
	// local row is changed by the sweep, and the skip is refused before it reaches here.
	if o.PlatformEventID != "" {
		if err := occurrenceCancelRefusal(o, now, s.destinationLive(o.DestinationID)); err != nil {
			return err
		}
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
			return fmt.Errorf("the event %s could not be cancelled: %w", o.PlatformEventID, err)
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
