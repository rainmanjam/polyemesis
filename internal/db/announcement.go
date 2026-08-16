package db

// Pre-announcement markers: what a destination has already told a platform
// about, so a sweep that runs every few minutes does not create the same event
// page twice.
//
// ALL OF THIS BEGAN IN facebook.go, and none of it is about Facebook. Read the
// rules below and count the sentences that need the word Graph: a marker is
// keyed by SCHEDULE because two schedules naming one destination are two shows;
// it carries an OCCURRENCE because "already announced" has to mean "for this
// occurrence"; an empty broadcast id is an in-flight create rather than an
// announcement; markers expire a day after their show; the ceiling evicts
// intents and never a live broadcast. Every one of those was paid for in
// duplicate public event pages, and every one of them is true of any platform
// that can create a broadcast ahead of the show.
//
// So the shape lives here on its own, and a platform's settings block embeds it
// -- see FacebookSettings. What is deliberately NOT shared is the rows: see
// AnnouncementSet.

import (
	"sort"
	"time"
)

// AnnouncementSet is everything one destination has announced on ONE platform.
//
// ONE SET PER PLATFORM, and that is not an accident of where it came from. A
// BroadcastID is a Facebook live_video id, addressable only with the Facebook
// token that created it. Pooling ids from two platforms into a single "neutral"
// list would strip the one fact that says which token can act on which id, and
// the first symptom would be a reschedule POSTed to the wrong platform's node --
// which Graph answers in a way that reads as success. What is worth sharing is
// this shape and the machinery on it, not the rows.
type AnnouncementSet struct {
	// Announcements is one marker per SHOW, not one per destination.
	//
	// One destination is reached by every start schedule that names it -- and a
	// schedule naming nothing names them all, which is the commonest shape --
	// so a single marker made two schedules fight over one broadcast: each
	// sweep saw the other's occurrence recorded, moved the one broadcast to its
	// own, and one of the two shows never got an event page at all.
	//
	// Keyed by SCHEDULE, carrying the occurrence. The schedule is what makes
	// "the same show, moved" distinguishable from "a different show": a weekly
	// show's next occurrence must MOVE last week's broadcast, while another
	// schedule's occurrence must get one of its own.
	Announcements []Announcement `json:"announcements,omitempty"`
	// ScheduledFor and BroadcastID are the single-pair marker Announcements
	// replaced. They are still WRITTEN, because they are how the destination
	// card links to an event page, and still READ, because rows written before
	// Announcements existed are already in operators' databases -- see merged.
	//
	// They mirror the soonest announced occurrence. Nothing may write them
	// directly: Announce keeps them in step.
	//
	// Zero means nothing has been announced.
	ScheduledFor time.Time `json:"scheduledFor,omitempty"`
	BroadcastID  string    `json:"broadcastId,omitempty"`
}

// Announcement is one show: the schedule that will start it, the occurrence it
// starts at, and the broadcast created for it.
type Announcement struct {
	// ScheduleID is the start schedule this broadcast belongs to.
	//
	// ZERO MEANS "written before this was keyed by schedule" -- the single-pair
	// marker folded in by merged. The first schedule that needs a broadcast
	// adopts it rather than creating a second one beside it, which is what
	// keeps an upgrade from duplicating every pre-announced event page.
	ScheduleID int64 `json:"scheduleId,omitempty"`
	// Occurrence is the instant the show starts. It is what makes "already
	// announced" mean "already announced for THIS occurrence": a weekly show
	// needs a new broadcast every week, and a boolean would be true forever
	// after the first one.
	Occurrence time.Time `json:"occurrence"`
	// BroadcastID is the live video created for that occurrence.
	//
	// EMPTY MEANS THE CREATE WAS IN FLIGHT and its result never reached this
	// row. The marker is written before the platform call precisely so that a
	// failed write leaves evidence: a real public broadcast with no local
	// record is one the next sweep would otherwise duplicate. It is not an
	// announcement -- AnnouncedFor says no -- it is a reason not to create a
	// second one.
	BroadcastID string `json:"broadcastId,omitempty"`
}

const (
	// announcementRetention is how long a marker outlives its occurrence.
	//
	// The list must not grow forever: a destination on a daily schedule passes
	// an occurrence every day, and a row that accumulated one entry per day
	// would be a JSON blob that only ever gets bigger. Entries are dropped once
	// their occurrence is this far in the past.
	//
	// A day rather than an instant because the marker is what the card links
	// to: a show that started this morning is still the one an operator is
	// looking at.
	announcementRetention = 24 * time.Hour
	// maxAnnouncements is a ceiling on the markers it is SAFE to drop, and it
	// is not the real bound -- announcementRetention is. It exists so that an
	// install whose schedules are all in the future cannot grow the row without
	// limit.
	//
	// IT NEVER EVICTS A MARKER THAT NAMES A LIVE BROADCAST, and an earlier
	// version of this did. That version sorted by occurrence and kept the tail,
	// so the entry it dropped was the show NEAREST IN TIME -- and dropping a
	// marker whose live_video still exists does not merely lose bookkeeping: the
	// next sweep finds no marker, creates a second event page, and drops another
	// marker to make room for it. People are already subscribed to the first.
	// It thrashes, one orphaned event page per sweep, and it starts with the
	// broadcast about to go out.
	//
	// Dropping the FURTHEST-out instead would only choose a quieter victim. Any
	// eviction of a live marker has the same shape.
	//
	// So the ceiling applies only to intents -- markers with no BroadcastID,
	// which record a create whose outcome never came back. Losing one of those
	// costs a retry, which is the behaviour without the marker anyway. If a row
	// is still over the ceiling once those are gone, it is kept: the set is
	// bounded by the number of enabled start schedules targeting one destination
	// inside a seven-day horizon, which an operator controls and which is a few
	// hundred bytes even when it is absurd. A slightly larger JSON blob is worth
	// less than a public event page nobody can find.
	maxAnnouncements = 32
)

// merged is every marker this set carries, including the single-pair one.
//
// The pair is folded in as an entry with no schedule id rather than migrated on
// read, so a row written by an earlier version keeps its announcement without a
// migration and without a write. It is skipped once Announce has copied it into
// the list, which is what stops one broadcast being counted twice.
func (s AnnouncementSet) merged() []Announcement {
	out := make([]Announcement, len(s.Announcements))
	copy(out, s.Announcements)
	if s.BroadcastID == "" {
		return out
	}
	for _, a := range out {
		if a.BroadcastID == s.BroadcastID {
			return out
		}
	}
	return append(out, Announcement{Occurrence: s.ScheduledFor, BroadcastID: s.BroadcastID})
}

// AnnouncedFor reports whether a broadcast has already been created for this
// exact occurrence, by ANY schedule.
//
// By any schedule on purpose: two schedules that happen to name the same
// instant are one show, and creating a second event page for it would notify
// the same subscribers twice.
//
// Equal rather than ==: these round-trip through JSON, and a time.Time carries
// a monotonic reading and a location that == compares and Equal does not.
func (s AnnouncementSet) AnnouncedFor(occurrence time.Time) bool {
	for _, a := range s.merged() {
		if a.BroadcastID != "" && a.Occurrence.Equal(occurrence) {
			return true
		}
	}
	return false
}

// AnnouncementFor is what this schedule has already done, if anything.
//
// A marker with no schedule id is offered to the first schedule that asks, and
// Announce then re-keys it -- so an upgraded row's existing broadcast is MOVED
// by whichever schedule claims it and every other schedule creates its own.
func (s AnnouncementSet) AnnouncementFor(scheduleID int64) (Announcement, bool) {
	var (
		legacy     Announcement
		haveLegacy bool
	)
	for _, a := range s.merged() {
		if a.ScheduleID == scheduleID {
			return a, true
		}
		if a.ScheduleID == 0 && a.BroadcastID != "" && !haveLegacy {
			legacy, haveLegacy = a, true
		}
	}
	return legacy, haveLegacy
}

// Announce records what this schedule has done for this occurrence, replacing
// whatever it had recorded before and dropping markers whose occurrence has
// passed.
//
// broadcastID may be empty, which records the INTENT to create one. See
// Announcement.BroadcastID.
//
// now is a parameter rather than time.Now() so the sweep's clock is the one
// that decides what has passed -- the same reason preannounceOnce takes one.
func (s *AnnouncementSet) Announce(scheduleID int64, occurrence time.Time, broadcastID string, now time.Time) {
	stale := now.Add(-announcementRetention)
	kept := make([]Announcement, 0, len(s.Announcements)+1)
	for _, a := range s.merged() {
		switch {
		case a.ScheduleID == scheduleID:
			// This schedule's previous marker, superseded by the one below.
		case broadcastID != "" && a.BroadcastID == broadcastID:
			// The same broadcast under an older key: the single-pair marker
			// being adopted. Dropped here, or a second schedule would adopt it
			// too and the two would move one broadcast back and forth.
		case a.Occurrence.Before(stale):
			// Its show has been and gone.
		default:
			kept = append(kept, a)
		}
	}
	kept = append(kept, Announcement{
		ScheduleID: scheduleID, Occurrence: occurrence, BroadcastID: broadcastID,
	})
	sort.Slice(kept, func(i, j int) bool { return kept[i].Occurrence.Before(kept[j].Occurrence) })
	s.Announcements = capIntents(kept)
	s.mirror()
}

// capIntents enforces maxAnnouncements by dropping INTENTS only -- markers
// with no BroadcastID, which record a create whose outcome never came back.
//
// in must already be sorted by occurrence, ascending.
//
// Losing an intent costs a retry, which is what would have happened without the
// marker at all. Losing a marker that names a live broadcast costs a public
// event page: the next sweep sees nothing, creates a second one, and evicts
// another marker to fit it. So if the row is still over the ceiling once every
// intent is gone, it is kept whole. See maxAnnouncements.
//
// The furthest-out intents go first. An intent close in time is about to be
// resolved one way or the other by the very next sweep; one seven days out has
// the longest to sit there being wrong.
func capIntents(in []Announcement) []Announcement {
	over := len(in) - maxAnnouncements
	if over <= 0 {
		return in
	}
	out := make([]Announcement, 0, len(in))
	// Walk from the furthest occurrence backwards, dropping intents until the
	// row fits, then keep everything else in its original order.
	drop := make(map[int]bool, over)
	for i := len(in) - 1; i >= 0 && over > 0; i-- {
		if in[i].BroadcastID == "" {
			drop[i] = true
			over--
		}
	}
	for i, a := range in {
		if !drop[i] {
			out = append(out, a)
		}
	}
	return out
}

// Forget drops this schedule's marker, and any marker naming this broadcast.
//
// Both, because the give-up path concludes a broadcast is gone while the
// schedule may still be holding it under the single-pair key it adopted it
// from -- forgetting only one of the two would leave the other behind and the
// fresh broadcast would never be created.
func (s *AnnouncementSet) Forget(scheduleID int64, broadcastID string) {
	kept := make([]Announcement, 0, len(s.Announcements))
	for _, a := range s.merged() {
		if a.ScheduleID == scheduleID || (broadcastID != "" && a.BroadcastID == broadcastID) {
			continue
		}
		kept = append(kept, a)
	}
	s.Announcements = kept
	s.mirror()
}

// mirror republishes the soonest announced occurrence into the single-pair
// fields, which are what the destination card links to.
//
// Derived rather than maintained: two places that both decide what the current
// broadcast is would eventually disagree, and the one the operator sees is the
// one that would be wrong.
func (s *AnnouncementSet) mirror() {
	s.ScheduledFor, s.BroadcastID = time.Time{}, ""
	for _, a := range s.Announcements {
		if a.BroadcastID != "" {
			s.ScheduledFor, s.BroadcastID = a.Occurrence, a.BroadcastID
			return
		}
	}
}
