package db

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
	// BackupIngest USED TO LIVE HERE and is now Destination.BackupIngestWanted.
	// A tombstone rather than nothing, because the field name survives in the
	// stored JSON of every install that had it -- MigrateDestinationExpertArgs
	// reads `$.backupIngest` out of this blob exactly once, on the pass that
	// adds the column -- and somebody will find it there and come looking.
	//
	// It gates BackupURL and BackupStreamKey, two columns that are on
	// Destination and not in here for a reason their own comment states: the
	// ENGINE consumes them, and the engine should not have to know which
	// platform a destination is. Leaving the bool behind meant the engine's
	// gate on those two went through this struct, and no second platform could
	// reach redundancy however it was configured.
	//
	// Facebook's create-time enable_backup_ingest parameter is unaffected and
	// still named for the platform -- see oauth.IngestOptions.BackupIngest.
	// That one is a fact about Facebook's API; this was the operator's intent.

	// The announcement markers. Nothing about them is specific to Facebook, so
	// they are a type of their own -- see announcement.go.
	//
	// EMBEDDED ANONYMOUSLY, AND LEFT AT THIS POSITION, and both halves are load
	// bearing on the wire. Anonymous with no JSON tag is what keeps
	// `announcements`, `scheduledFor` and `broadcastId` as top-level keys of the
	// `facebook` column, which is the shape every stored row already has; a tag
	// here would nest them and orphan the lot (see AnnouncementSet for what that
	// costs). The position is the order encoding/json writes those keys in --
	// which no decoder cares about, but holding it makes a row re-encoded by this
	// build byte-identical to the one it was read from, and that is what lets
	// TestAStoredFacebookBlockReEncodesByteForByte compare bytes instead of
	// comparing a struct against itself and proving nothing.
	AnnouncementSet
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
	// The backup intent used to be a third clause here, because it was a
	// create-time parameter stored in this struct and dropUnsendableSettings
	// would otherwise have read a backup-enabled destination as unconfigured.
	// It is no longer in this struct, and it must not be reintroduced through
	// this method: it is not a Facebook setting, so a destination that switches
	// platform keeps it -- which is the whole point. What still belongs here is
	// what Facebook's create call and nothing else can act on.
	//
	// The announcement markers stay excluded for the older reason: Empty asks
	// "is there anything to SEND at create time", and a marker is bookkeeping.
	// They are now a whole embedded struct rather than three fields, which makes
	// the omission easier to read as an oversight than it was -- it is not.
	return len(f.Crosspost) == 0 && f.DonateCharityID == ""
}
