package oauth

import "github.com/rainmanjam/polyemesis/internal/db"

// Platform limits are the caps a broadcast runs into on the platform's side,
// rather than anything polyemesis chooses. Wave 3 rollover (#19) reads them to
// count down to a rollover and to plan one before the cap lands. They are kept
// apart from the capability matrix on purpose: the matrix is served to the UI
// and mirrored in ui/src/lib/capabilities.ts with a drift guard, and a limit
// table that was added as a matrix column would reshape both at once. Lanes
// that show a limit on a card read it from here.
//
// The sourcing rule is the matrix's own. A stated limit carries an https link.
// A limit nobody has confirmed is LimitUnverified and has no number, so a
// countdown never runs on a guess.

// LimitKind names what the limit measures.
type LimitKind string

const (
	// LimitKindBroadcastDuration is the longest single live broadcast the
	// platform will carry before it ends the stream itself.
	LimitKindBroadcastDuration LimitKind = "broadcastDuration"
	// LimitKindArchiveDuration is the longest broadcast the platform will keep
	// as a replay. A longer one may not be archived at all.
	LimitKindArchiveDuration LimitKind = "archiveDuration"
)

// LimitStatus says how well a limit is established.
type LimitStatus string

const (
	// LimitDocumented: the number was read on the platform's own page or API
	// reference during the check that recorded it.
	LimitDocumented LimitStatus = "documented"
	// LimitReported: the number is what the platform's guidelines are reported
	// to say, from a search result, and the page itself was not read in the
	// check. Re-verify before a release depends on it.
	LimitReported LimitStatus = "reported"
	// LimitUnverified: no platform source was confirmed. Hours is zero and no
	// countdown should be shown for it.
	LimitUnverified LimitStatus = "unverified"
)

// PlatformLimit is one published cap on one platform.
type PlatformLimit struct {
	PresetID string      `json:"presetId"`
	Platform db.Platform `json:"platform,omitempty"`
	Kind     LimitKind   `json:"kind"`
	Hours    float64     `json:"hours"`
	Status   LimitStatus `json:"status"`
	Source   string      `json:"source,omitempty"`
	Note     string      `json:"note,omitempty"`
}

// PlatformLimits returns the limit table in display order.
func PlatformLimits() []PlatformLimit {
	return []PlatformLimit{
		{
			PresetID: "twitch", Platform: db.PlatformTwitch,
			Kind: LimitKindBroadcastDuration, Hours: 48, Status: LimitReported,
			Source: "https://help.twitch.tv/s/article/broadcasting-guidelines?language=en_US",
			Note:   "Twitch's Broadcasting Guidelines give 48 hours as the maximum broadcast length and say it cannot be changed. The guideline page itself did not load in the check that recorded this, so it is reported, not read. Rollover is a reconnect, not an end-and-create, because Twitch publishes no broadcast lifecycle API (see the Start / end column).",
		},
		{
			PresetID: "youtube", Platform: db.PlatformYouTube,
			Kind: LimitKindArchiveDuration, Hours: 12, Status: LimitDocumented,
			Source: "https://support.google.com/youtube/answer/9296823?hl=en",
			Note:   "The DVR page says DVR capabilities may be limited or unavailable for streams longer than 12 hours. The same page's archive sentence (a stream under 12 hours can be archived automatically) was read from a search excerpt, not the full page. A longer stream may not be captured as a replay.",
		},
		{
			PresetID: "facebook", Platform: db.PlatformFacebook,
			Kind: LimitKindBroadcastDuration, Hours: 8, Status: LimitDocumented,
			Source: "https://developers.facebook.com/docs/live-video-api/reference",
			Note:   "Meta's live video API reference lists \"Not exceed 8 hours\" among the requirements for a successful broadcast. Older sources cite 4 hours; the current reference is the one recorded here.",
		},
		{
			PresetID: "kick", Platform: db.PlatformKick,
			Kind: LimitKindBroadcastDuration, Hours: 0, Status: LimitUnverified,
			Note: "No platform source was confirmed. Third-party guides disagree: some say Kick has no stream length limit, and one says only the first 48 hours of a stream are saved as a replay. Replay retention is also reported as 30 days for verified and 7 days for unverified channels, which conflicts with the 48-hour figure. Verify against Kick's help centre before adding a countdown.",
		},
	}
}
