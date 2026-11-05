package oauth

import (
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

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
			Source: "https://support.google.com/youtube/answer/6247592?hl=en",
			Note:   "YouTube's archive help page says a stream over 12 hours may not be captured at all, so a longer broadcast can lose its replay. The DVR page gives the same 12-hour figure but states the consequence only as DVR being limited, so it is not the citation used here. Rollover ends and restarts the broadcast before this point.",
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
		{
			// No Platform: Instagram has no integration behind it, so its
			// destinations save as custom and RolloverLimitFor never hands
			// this row to one. It is listed so the cap is on record with its
			// source, and so the limits route can show it.
			PresetID: "instagram",
			Kind:     LimitKindBroadcastDuration, Hours: 1, Status: LimitReported,
			Source: "https://support.restream.io/en/articles/18180232-stream-to-instagram-with-a-stream-key",
			Note:   "Restream's Instagram help (read 2026-11-05) says Instagram limits a live video to one hour and that a longer stream may end automatically. Reported rather than documented because the page is Restream's, not Meta's. polyemesis cannot start the next Instagram live video, so there is no automatic rollover: plan a broadcast as one-hour segments.",
		},
	}
}

// RolloverLimitFor returns the limit a countdown and a planned rollover count
// to for one platform. When a platform has both a broadcast-duration cap and an
// archive cap, the one that ends or truncates the live feed is used: YouTube's
// rollover is driven by its archive cap, because that is the point at which a
// long stream stops being kept, while its live feed carries on. ok is false for a
// platform the table does not describe, which gets no countdown.
func RolloverLimitFor(p db.Platform) (PlatformLimit, bool) {
	var found PlatformLimit
	ok := false
	for _, l := range PlatformLimits() {
		// A row with no platform (Instagram's) is on record but drives no
		// countdown: matching it against an empty platform would hand its cap
		// to any destination whose platform was never set.
		if l.Platform == "" || l.Platform != p {
			continue
		}
		if l.Kind == LimitKindArchiveDuration {
			return l, true
		}
		found, ok = l, true
	}
	return found, ok
}

// CutoffFor returns the instant a broadcast that went live at liveSince reaches
// this limit. ok is false, and no time is returned, when there is nothing honest
// to count to: the limit is unverified or has no positive number, or the live
// start is unknown. The zero time is treated as unknown, because a countdown
// counted from it would read as long overdue rather than as no answer.
func (l PlatformLimit) CutoffFor(liveSince time.Time) (time.Time, bool) {
	if l.Status == LimitUnverified || l.Hours <= 0 {
		return time.Time{}, false
	}
	if liveSince.IsZero() {
		return time.Time{}, false
	}
	return liveSince.Add(time.Duration(l.Hours * float64(time.Hour))), true
}
