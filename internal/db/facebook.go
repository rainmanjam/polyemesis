package db

import (
	"sort"
	"time"
)

// FacebookSettings is per-destination Facebook configuration applied when the
// broadcast is CREATED rather than pushed afterwards.
//
// These live on the destination and not in the composer because they are opaque
// ids an operator fetches once from Facebook's own console and then reuses --
// which Page to share with, which charity to collect for -- and because the
// create edge is the surface Meta documents. The Graph API reference has no
// Updating section for LiveVideo at all, so pushing them later would be building
// on an endpoint whose accepted parameters are written down nowhere.
type FacebookSettings struct {
	// Crosspost names the Pages this broadcast is shared with.
	Crosspost []CrosspostTarget `json:"crosspost,omitempty"`
	// DonateCharityID adds a donate button for one charity.
	DonateCharityID string `json:"donateCharityId,omitempty"`
	// BackupIngest asks Facebook to provision a secondary ingest endpoint at
	// create time, and publishes a redundant feed to it so a dropped
	// connection does not drop the broadcast.
	//
	// Off by default: it doubles this destination's upload bandwidth and its
	// audio encoding cost, which an operator on a thin or metered uplink has
	// to choose deliberately.
	//
	// TURNING IT ON COSTS ONE RECONNECT, and that is unavoidable rather than
	// sloppy. A backup endpoint exists only on a broadcast created with it,
	// and IngestFor creates a new live_video on every call -- so obtaining one
	// replaces the primary stream key, which is part of Target(), which is in
	// destSpec. Enable it before going live.
	BackupIngest bool `json:"backupIngest,omitempty"`
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
// starts at, and the Facebook broadcast created for it.
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
	// row. The marker is written before the Graph call precisely so that a
	// failed write leaves evidence: a real public live_video with no local
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
	// maxAnnouncements is the hard ceiling, and it is not the real bound --
	// announcementRetention is. It exists so that an install whose schedules
	// are all in the future cannot grow the row without limit.
	//
	// Reaching it needs 32 distinct occurrences inside Facebook's seven-day
	// horizon on ONE destination, i.e. 32 enabled start schedules that all
	// target it. Past that the oldest is dropped, which costs a duplicate event
	// page for the show nearest in time -- worth stating rather than pretending
	// the ceiling is free.
	maxAnnouncements = 32
)

// merged is every marker this row carries, including the single-pair one.
//
// The pair is folded in as an entry with no schedule id rather than migrated on
// read, so a row written by an earlier version keeps its announcement without a
// migration and without a write. It is skipped once Announce has copied it into
// the list, which is what stops one broadcast being counted twice.
func (f FacebookSettings) merged() []Announcement {
	out := make([]Announcement, len(f.Announcements))
	copy(out, f.Announcements)
	if f.BroadcastID == "" {
		return out
	}
	for _, a := range out {
		if a.BroadcastID == f.BroadcastID {
			return out
		}
	}
	return append(out, Announcement{Occurrence: f.ScheduledFor, BroadcastID: f.BroadcastID})
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
func (f FacebookSettings) AnnouncedFor(occurrence time.Time) bool {
	for _, a := range f.merged() {
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
func (f FacebookSettings) AnnouncementFor(scheduleID int64) (Announcement, bool) {
	var (
		legacy     Announcement
		haveLegacy bool
	)
	for _, a := range f.merged() {
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
func (f *FacebookSettings) Announce(scheduleID int64, occurrence time.Time, broadcastID string, now time.Time) {
	stale := now.Add(-announcementRetention)
	kept := make([]Announcement, 0, len(f.Announcements)+1)
	for _, a := range f.merged() {
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
	if len(kept) > maxAnnouncements {
		kept = kept[len(kept)-maxAnnouncements:]
	}
	f.Announcements = kept
	f.mirror()
}

// Forget drops this schedule's marker, and any marker naming this broadcast.
//
// Both, because the give-up path concludes a broadcast is gone while the
// schedule may still be holding it under the single-pair key it adopted it
// from -- forgetting only one of the two would leave the other behind and the
// fresh broadcast would never be created.
func (f *FacebookSettings) Forget(scheduleID int64, broadcastID string) {
	kept := make([]Announcement, 0, len(f.Announcements))
	for _, a := range f.merged() {
		if a.ScheduleID == scheduleID || (broadcastID != "" && a.BroadcastID == broadcastID) {
			continue
		}
		kept = append(kept, a)
	}
	f.Announcements = kept
	f.mirror()
}

// mirror republishes the soonest announced occurrence into the single-pair
// fields, which are what the destination card links to.
//
// Derived rather than maintained: two places that both decide what the current
// broadcast is would eventually disagree, and the one the operator sees is the
// one that would be wrong.
func (f *FacebookSettings) mirror() {
	f.ScheduledFor, f.BroadcastID = time.Time{}, ""
	for _, a := range f.Announcements {
		if a.BroadcastID != "" {
			f.ScheduledFor, f.BroadcastID = a.Occurrence, a.BroadcastID
			return
		}
	}
}

// CrosspostTarget is one Page and what to do with it.
type CrosspostTarget struct {
	PageID string `json:"pageId"`
	// CreatePost also publishes a post as that Page rather than only enabling
	// the share. Facebook's two actions -- enable_crossposting and
	// enable_crossposting_and_create_post -- differ by exactly this, so a lost
	// flag is a post nobody asked for.
	CreatePost bool `json:"createPost,omitempty"`
}

// Empty reports whether there is nothing to send.
func (f FacebookSettings) Empty() bool {
	// BackupIngest counts, unlike the announcement markers above. Empty asks
	// "is there anything to SEND at create time", and this is a create-time
	// parameter -- whereas an announcement is bookkeeping. Without it
	// dropUnsendableSettings would read a backup-enabled destination as having
	// nothing configured.
	return len(f.Crosspost) == 0 && f.DonateCharityID == "" && !f.BackupIngest
}
