package db

import "time"

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
	// ScheduledFor is the occurrence a broadcast has already been announced
	// for, NOT a flag.
	//
	// A weekly show needs a new broadcast every week, so "already done" has to
	// mean "already done for THIS occurrence". A boolean would be true forever
	// after the first one, and every week after that would get no event page.
	//
	// Zero means nothing has been announced.
	ScheduledFor time.Time `json:"scheduledFor,omitempty"`
	// BroadcastID is the Facebook live video created for that occurrence. It is
	// what a reschedule edits and what the UI links to.
	BroadcastID string `json:"broadcastId,omitempty"`
}

// AnnouncedFor reports whether a broadcast has already been created for this
// exact occurrence.
//
// Equal rather than ==: these round-trip through JSON, and a time.Time carries
// a monotonic reading and a location that == compares and Equal does not.
func (f FacebookSettings) AnnouncedFor(occurrence time.Time) bool {
	return f.BroadcastID != "" && f.ScheduledFor.Equal(occurrence)
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
	// BackupIngest counts, unlike ScheduledFor/BroadcastID above. Empty asks
	// "is there anything to SEND at create time", and this is a create-time
	// parameter -- whereas the announcement marker is bookkeeping. Without it
	// dropUnsendableSettings would read a backup-enabled destination as having
	// nothing configured.
	return len(f.Crosspost) == 0 && f.DonateCharityID == "" && !f.BackupIngest
}
