package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/platform"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// ============================================================================
// WHAT DELETING A DESTINATION OR A PROGRAMME TAKES WITH IT, OUTSIDE ITS ROW.
//
// Both delete handlers -- a destination, and a programme whose destinations go
// by ON DELETE CASCADE -- call the same two hooks, so a new kind of reference
// is added in one place and reaches both:
//
//	beforeDestinationsRemoved  while the rows, their accounts and tokens exist:
//	                           the note about a stranded YouTube stream, and the
//	                           platform events created for them.
//	(the store's delete)       schedules.destination_ids and the rollover row,
//	                           in the delete's own transaction (db.SchedulePrune).
//	afterDestinationsRemoved   once they are gone: the OBS bridge document.
//
// A buffered broadcast delay is cut by the destination delete itself
// (CutOnStop, CutDelay). A programme delete needs neither: the engine treats a
// destination whose row is gone as cut (rowsStillThere), pinned by
// TestDeletingAProgrammeCutsItsDelayedDestinationsLikeDeletingThem.
//
// Every step is best-effort and none of them can refuse the delete: the
// operator asked for the row to go, and a platform that is down or a token
// that lapsed must not keep it. What could not be done comes back as a
// warning naming the thing left behind, so the operator can remove it by hand.
// ============================================================================

// destinationEventsTimeout bounds the platform calls one delete makes. A slow
// platform makes the delete slow, never stuck: whatever is not cancelled by
// then is named in a warning and left for the operator.
const destinationEventsTimeout = 30 * time.Second

// beforeDestinationsRemoved runs while the destinations still exist.
func (s *Server) beforeDestinationsRemoved(ctx context.Context, ds []*db.Destination, now time.Time) []string {
	// What goes in this request: a sibling that is also being deleted is not
	// still using a shared key.
	going := make(map[int64]bool, len(ds))
	for _, d := range ds {
		going[d.ID] = true
	}
	for _, d := range ds {
		s.noteOrphanedIngestStream(d, going)
	}
	return s.cancelDestinationEvents(ctx, ds, now)
}

// afterDestinationsRemoved runs once the rows are gone. prune is what the
// store's delete did to schedules.
func (s *Server) afterDestinationsRemoved(prune db.SchedulePrune) []string {
	s.pruneOBSReferences()
	return schedulePruneWarnings(prune)
}

// schedulePruneWarnings says what the delete did to the schedules that named
// the destination. Past tense: it has happened, and nothing is being asked.
func schedulePruneWarnings(p db.SchedulePrune) []string {
	var out []string
	if n := len(p.Disabled); n > 0 {
		out = append(out, fmt.Sprintf("%s switched off, because every destination %s acted on was deleted: %s. "+
			"Pick other destinations and switch %s back on, or delete %s.",
			plural(n, "schedule was", "schedules were"), wordFor(n, "it", "they"),
			quoteNames(p.Disabled), wordFor(n, "it", "them"), wordFor(n, "it", "them")))
	}
	if n := len(p.Narrowed); n > 0 {
		out = append(out, fmt.Sprintf("%s no longer %s the deleted destination: %s.",
			plural(n, "schedule", "schedules"), wordFor(n, "names", "name"), quoteNames(p.Narrowed)))
	}
	return out
}

// wordFor is plural without the count.
func wordFor(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func quoteNames(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(q, ", ")
}

// cancelDestinationEvents withdraws the scheduled platform events created for
// these destinations, before their rows go.
//
// BEFORE, because after the delete nothing can: an occurrence row carries the
// destination id and no account, so once the destination row is gone there is
// no token to cancel with. The sweep used to find such a row every five
// minutes, fail to reach the account, and retry until the row aged out 30
// days after the show -- while the event page sat on the channel announcing a
// show no destination would ever air.
//
// Two kinds of event:
//
//   - The recurring-template occurrences (recurring_occurrences), cancelled
//     through cancelOccurrence, which keeps its own refusals: a show that has
//     started, or a destination that is on air, is not deleted from here.
//   - The legacy pre-announce markers on the destination itself, for a
//     schedule with no template. A platform that can cancel a scheduled event
//     (YouTube) is asked to; one that cannot (Facebook offers no such call) is
//     named in a warning with the event id. A past marker is a show that
//     already aired and is left alone, as the sweep leaves it.
//
// Best-effort and bounded by destinationEventsTimeout. Detached from the
// request's cancellation, so closing the tab mid-delete does not leave half
// the events behind.
func (s *Server) cancelDestinationEvents(ctx context.Context, ds []*db.Destination, now time.Time) []string {
	if len(ds) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), destinationEventsTimeout)
	defer cancel()

	open, err := s.store.ListOpenOccurrences()
	if err != nil {
		s.log.Warn("destination delete: cannot read scheduled events", "err", redactErr(err))
		return []string{"The scheduled events created for the deleted destination could not be read, so any " +
			"already on the platform were left there; check the platform's own studio."}
	}
	var warnings []string
	for _, d := range ds {
		covered := map[string]bool{}
		warnings = append(warnings, s.cancelDestinationOccurrences(ctx, d, open, covered, now)...)
		warnings = append(warnings, s.cancelDestinationMarkers(ctx, d, covered, now)...)
	}
	return warnings
}

// cancelDestinationOccurrences cancels the destination's upcoming scheduled
// events, and returns a warning for each one the platform would not let go.
func (s *Server) cancelDestinationOccurrences(ctx context.Context, d *db.Destination, open []scheduler.Occurrence,
	covered map[string]bool, now time.Time) []string {
	var warnings []string
	for _, o := range open {
		if o.DestinationID != d.ID || o.PlatformEventID == "" {
			continue
		}
		if o.State != scheduler.OccurrenceUpcoming && o.State != scheduler.OccurrenceStranded {
			continue
		}
		// Covered before the time check, so a pre-announce marker for the
		// same event is never taken up by the loop below either.
		covered[o.PlatformEventID] = true
		// A show that already aired is left alone, as its marker is below.
		// Its row stays `upcoming` until the sweep ages it out, 30 days
		// after the show (occurrenceRetention), and cancelOccurrence would
		// only refuse it: every recent show would be reported "left on the
		// platform", and turn a clean programme delete from 204 into 200.
		if !o.At.After(now) {
			continue
		}
		if err := s.cancelOccurrence(ctx, o, true, now); err != nil {
			s.log.Warn("destination delete: a scheduled event was left on the platform",
				"destination", d.Name, "event", o.PlatformEventID, "err", redactErr(err))
			warnings = append(warnings, eventLeftWarning(d, o.PlatformEventID, redactErr(err)))
			continue
		}
		s.log.Info("destination delete: cancelled a scheduled event", "destination", d.Name,
			"event", o.PlatformEventID)
	}
	return warnings
}

// cancelDestinationMarkers cancels the destination's pre-announced events that no
// occurrence row covers, and returns a warning for each one left on the platform.
func (s *Server) cancelDestinationMarkers(ctx context.Context, d *db.Destination, covered map[string]bool,
	now time.Time) []string {
	var warnings []string
	for _, a := range d.Facebook.Broadcasts() {
		if covered[a.BroadcastID] || !a.Occurrence.After(now) {
			continue
		}
		if err := s.cancelMarker(ctx, d, a.BroadcastID); err != nil {
			s.log.Warn("destination delete: a pre-announced event was left on the platform",
				"destination", d.Name, "event", a.BroadcastID, "err", redactErr(err))
			warnings = append(warnings, eventLeftWarning(d, a.BroadcastID, redactErr(err)))
			continue
		}
		s.log.Info("destination delete: cancelled a pre-announced event", "destination", d.Name,
			"event", a.BroadcastID)
	}
	return warnings
}

// errNoCancelCall is a platform that can create a scheduled event and offers
// no call to remove one (Facebook).
var errNoCancelCall = fmt.Errorf("the platform offers no call to remove a scheduled event from here")

// cancelMarker removes one pre-announced event of a destination about to be
// deleted. The same rule as an occurrence: never while the destination is on
// air, because the live broadcast may be this very event.
func (s *Server) cancelMarker(ctx context.Context, d *db.Destination, broadcastID string) error {
	sb, ok := oauth.As[oauth.ScheduledBroadcaster](s.providers, d.Platform)
	if !ok {
		return errNoCancelCall
	}
	ed, ok := sb.(oauth.ScheduledEditor)
	if !ok {
		return errNoCancelCall
	}
	if d.AccountID == nil {
		return fmt.Errorf("the destination has no connected account to cancel with")
	}
	if s.destinationLive(d.ID) {
		return errDestinationDelivering
	}
	acct, err := s.tokenFor(ctx, *d.AccountID)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, templateCallTimeout)
	defer cancel()
	return ed.CancelScheduledBroadcast(cctx, acct.AccessToken, broadcastID)
}

// eventLeftWarning names an event the delete could not remove, and where to
// remove it by hand.
func eventLeftWarning(d *db.Destination, eventID, reason string) string {
	return fmt.Sprintf("%q: the scheduled event %s was left on %s (%s). Remove it in %s's own studio "+
		"if it is not wanted.", d.Name, eventID, platformTitle(d.Platform), reason, platformTitle(d.Platform))
}

// platformTitle is a platform's name as its own site writes it, for a sentence
// an operator reads.
func platformTitle(p db.Platform) string {
	if row, ok := platform.ByEnum(string(p)); ok && p != db.PlatformCustom {
		return row.ShortName
	}
	return "the platform"
}
