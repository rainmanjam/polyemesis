package oauth

// Capability matrix: what a user actually gets from each platform, said out
// loud before they spend an hour on setup.
//
// Every platform polyemesis can stream to sits somewhere on a spectrum. At one
// end YouTube, Twitch and Facebook sign in and hand over a stream key. At the
// other end Instagram, which has no live-video API at all and whose RTMP path
// was removed for most accounts — a destination there would never connect, and
// "never connects" is the single most expensive failure a self-hosted tool can
// ship, because it looks exactly like a bug in the tool. In between sits Kick:
// full OAuth, chat, moderation and viewer stats, and no stream-key endpoint
// anywhere in its published API.
//
// The matrix exists because that spectrum was previously only legible to
// someone who read internal/oauth. It is a description of reality, not a gate:
// nothing here is consulted before attempting an operation. See SupportUnknown
// below — a capability we could not verify is reported as unverified and tried
// anyway, because this repo has learned five times that a check wrong in the
// restrictive direction is worse than no check at all.
//
// Sourcing rule for anyone editing this file: every SupportNo must trace to a
// published API that was checked and found not to contain the thing. If you
// cannot point at the check, the answer is SupportUnknown. "I don't think that
// exists" is not a source, and a wrong SupportNo becomes a refusal the operator
// cannot argue with.

import "github.com/rainmanjam/polyemesis/internal/db"

// Capability is one column of the matrix.
type Capability string

const (
	// CapSSO is signing in with the platform, as opposed to pasting secrets.
	CapSSO Capability = "sso"
	// CapStreamKey is polyemesis fetching the ingest URL and key itself.
	CapStreamKey Capability = "streamKey"
	// CapMetadata is pushing title, description and category at go-live.
	CapMetadata Capability = "metadata"
	CapChatRead Capability = "chatRead"
	CapChatSend Capability = "chatSend"
	// CapModeration is deleting a message or timing out a user.
	CapModeration Capability = "moderation"
	// CapViewerStats is the live viewer count read back from the platform.
	CapViewerStats Capability = "viewerStats"
)

// Support is how well one platform does one thing.
//
// Four values rather than a boolean because the interesting platforms are not
// binary. Kick's stream key is not "unsupported" — it works perfectly, the
// operator just types it. Rumble's OAuth is not "unsupported" either; it is
// undocumented, which is a different sentence and a different UI.
type Support string

const (
	// SupportYes means polyemesis does this today.
	SupportYes Support = "yes"
	// SupportManual means it works, with the operator doing one step by hand.
	// A manual stream key is a supported destination, not a degraded one.
	SupportManual Support = "manual"
	// SupportNo means the platform publishes no API for it, so no amount of
	// setup will produce it. Reserved for things actually checked; see the
	// sourcing rule at the top of this file.
	SupportNo Support = "no"
	// SupportUnknown means polyemesis does not do this today and the platform's
	// API was not verified either way. It is the honest default and the
	// fail-open one: the UI shows it as unverified rather than as a refusal,
	// and nothing in the codebase treats it as permission to deny an attempt.
	SupportUnknown Support = "unknown"
)

// Tier is the one-glance summary of a row, for sorting and for a badge.
type Tier string

const (
	// TierIntegrated: sign in and polyemesis fetches the key.
	TierIntegrated Tier = "integrated"
	// TierPartial: sign in for everything except the key, which is pasted.
	// Kick is the whole reason this tier exists.
	TierPartial Tier = "partial"
	// TierManual: paste a URL and a key; there is no integration to connect.
	TierManual Tier = "manual"
	// TierUnsupported: polyemesis cannot stream here at all, and says so
	// instead of offering a destination that will never connect.
	TierUnsupported Tier = "unsupported"
)

// CapabilityInfo describes one column, so the UI does not hard-code labels and
// a scripted client can render the same table.
type CapabilityInfo struct {
	Key   Capability `json:"key"`
	Label string     `json:"label"`
	Help  string     `json:"help"`
}

// CapabilityColumns returns the columns in display order.
func CapabilityColumns() []CapabilityInfo {
	return []CapabilityInfo{
		{CapSSO, "Sign in", "Connect the account with OAuth instead of pasting secrets."},
		{CapStreamKey, "Stream key", "polyemesis fetches the ingest URL and key for you."},
		{CapMetadata, "Metadata push", "Set the title, description or category when you go live."},
		{CapChatRead, "Chat read", "Messages appear in the unified chat pane."},
		{CapChatSend, "Chat send", "You can reply from the chat pane."},
		{CapModeration, "Moderation", "Delete a message or time a viewer out."},
		{CapViewerStats, "Viewer stats", "Live viewer count read back from the platform."},
	}
}

// SupportInfo describes one legend entry.
type SupportInfo struct {
	Key   Support `json:"key"`
	Label string  `json:"label"`
	Help  string  `json:"help"`
}

// SupportLegend returns the legend, in the order it should be displayed.
func SupportLegend() []SupportInfo {
	return []SupportInfo{
		{SupportYes, "Works", "polyemesis does this for you today."},
		{SupportManual, "By hand", "Supported, with one step you do yourself — usually pasting a key."},
		{SupportUnknown, "Unverified", "Not built yet, and the platform's API was not confirmed either way. Nothing stops you trying."},
		{SupportNo, "Not possible", "The platform publishes no API for this, so no amount of setup will produce it."},
	}
}

// TierInfo describes one tier for a badge and a heading.
type TierInfo struct {
	Key   Tier   `json:"key"`
	Label string `json:"label"`
	Help  string `json:"help"`
}

// TierLegend returns the tiers in descending order of integration.
func TierLegend() []TierInfo {
	return []TierInfo{
		{TierIntegrated, "Fully integrated", "Sign in once and polyemesis fetches the ingest URL and stream key."},
		{TierPartial, "Sign in + paste key", "Sign-in works and brings chat and metadata with it, but the key is typed by hand."},
		{TierManual, "Manual key", "Paste the ingest URL and stream key from the platform's dashboard. Streaming works exactly as well; there is just nothing to connect."},
		{TierUnsupported, "Not supported", "polyemesis cannot stream here. Shown so you do not spend an evening finding that out."},
	}
}

// PlatformCapability is one row of the matrix.
type PlatformCapability struct {
	// PresetID matches an entry in db.DestinationPresets(), which is how the UI
	// joins a row to the destination picker.
	PresetID string `json:"presetId"`
	Name     string `json:"name"`
	// Platform is set only where there is integration code behind the name.
	// Empty means the destination saves as db.PlatformCustom.
	Platform db.Platform `json:"platform,omitempty"`
	Tier     Tier        `json:"tier"`
	// Summary is the row in one sentence, written for someone deciding whether
	// to start.
	Summary string `json:"summary"`
	// ReadFirst is the thing that will cost the operator a day if they meet it
	// halfway through instead of at the start. Facebook's App Review is the
	// reason this field exists.
	ReadFirst string                 `json:"readFirst,omitempty"`
	Caps      map[Capability]Support `json:"capabilities"`
	// Reasons explains a cell where the value alone would raise a question.
	// Only the cells that need it; a wall of tooltips is not honesty.
	Reasons map[Capability]string `json:"reasons,omitempty"`
	HelpURL string                `json:"helpUrl,omitempty"`
}

// Get returns the support level for one capability.
//
// An absent key is SupportUnknown rather than SupportNo, so a row that forgets
// a column reads as unverified instead of silently claiming the platform
// cannot do something.
func (p PlatformCapability) Get(c Capability) Support {
	if s, ok := p.Caps[c]; ok {
		return s
	}
	return SupportUnknown
}

// Streamable reports whether polyemesis can publish to this platform at all.
// It is for labelling the picker, not for blocking a save: an operator who has
// an ingest URL we do not know about should be able to use it.
func (p PlatformCapability) Streamable() bool { return p.Tier != TierUnsupported }

// platformCapabilities is the matrix. Order is display order: most integrated
// first, unsupported last, because the last row is the one nobody should have
// to scroll to find.
var platformCapabilities = []PlatformCapability{
	{
		PresetID: "youtube", Name: "YouTube Live", Platform: db.PlatformYouTube,
		Tier:    TierIntegrated,
		Summary: "Connect a Google account and polyemesis fetches the ingest URL and stream key, pushes your title and description at go-live, and reads and replies to live chat.",
		HelpURL: "https://support.google.com/youtube/answer/2907883",
		Caps: map[Capability]Support{
			CapSSO:         SupportYes,
			CapStreamKey:   SupportYes,
			CapMetadata:    SupportYes,
			CapChatRead:    SupportYes,
			CapChatSend:    SupportYes,
			CapModeration:  SupportUnknown,
			CapViewerStats: SupportUnknown,
		},
		Reasons: map[Capability]string{
			CapChatRead: "Polled against the Data API's daily quota, which polyemesis paces. A long broadcast can exhaust it; the chat pane says so with the reset time rather than going quiet.",
		},
	},
	{
		PresetID: "twitch", Name: "Twitch", Platform: db.PlatformTwitch,
		Tier:    TierIntegrated,
		Summary: "Connect a Twitch account and polyemesis fetches the stream key, sets your title and category at go-live, and joins chat over IRC.",
		HelpURL: "https://dev.twitch.tv/console/apps",
		Caps: map[Capability]Support{
			CapSSO:         SupportYes,
			CapStreamKey:   SupportYes,
			CapMetadata:    SupportYes,
			CapChatRead:    SupportYes,
			CapChatSend:    SupportYes,
			CapModeration:  SupportUnknown,
			CapViewerStats: SupportUnknown,
		},
		Reasons: map[Capability]string{
			CapMetadata: "Title and category, over the channel:manage:broadcast scope.",
		},
	},
	{
		PresetID: "facebook", Name: "Facebook Live", Platform: db.PlatformFacebook,
		Tier:    TierIntegrated,
		Summary: "Connect a Facebook profile or Page and polyemesis creates the broadcast, splits out the RTMPS ingest and key, pushes the title and description, and reads the comment thread.",
		// Up front, not in step six. Meta's review is measured in days and it
		// is the operator's to do — a setup guide that mentions it late has
		// already wasted their evening.
		ReadFirst: "Meta requires App Review before anyone other than you can connect an account. Your own account works immediately as a developer or tester of your app, which is all a single-operator setup needs — but publishing on someone else's behalf needs Advanced Access to publish_video (profiles) or pages_manage_posts plus pages_read_engagement (Pages). Budget days, not minutes, and start it before you need it.",
		HelpURL:   "https://developers.facebook.com/apps",
		Caps: map[Capability]Support{
			CapSSO:         SupportYes,
			CapStreamKey:   SupportYes,
			CapMetadata:    SupportYes,
			CapChatRead:    SupportYes,
			CapChatSend:    SupportUnknown,
			CapModeration:  SupportUnknown,
			CapViewerStats: SupportUnknown,
		},
		Reasons: map[Capability]string{
			CapStreamKey: "Facebook issues a fresh ingest and key per broadcast, so connecting the account is what creates the broadcast. There is no permanent key to reuse.",
			CapMetadata:  "Title and description. Facebook removed overlay_url in Graph API v24.0, so there is no overlay field to push.",
			CapChatRead:  "Facebook's live chat is the comment thread on the live video, read over the Graph API. A destination whose key was pasted by hand has no live-video id to attach to, and the chat pane says so.",
		},
	},
	{
		PresetID: "kick", Name: "Kick", Platform: db.PlatformKick,
		Tier:      TierPartial,
		Summary:   "Sign in with Kick for chat, moderation, metadata and viewer stats — then paste the stream key, because Kick's public API does not publish one.",
		ReadFirst: "Both halves of this destination are real at once: click Connect account for everything Kick's API does offer, and paste the ingest URL and key from Kick → Settings → Stream. Neither replaces the other, and the paste is not a workaround for a broken connection.",
		HelpURL:   "https://kick.com/dashboard/settings/stream",
		Caps: map[Capability]Support{
			CapSSO:         SupportYes,
			CapStreamKey:   SupportManual,
			CapMetadata:    SupportYes,
			CapChatRead:    SupportYes,
			CapChatSend:    SupportYes,
			CapModeration:  SupportYes,
			CapViewerStats: SupportYes,
		},
		Reasons: map[Capability]string{
			CapSSO:       "OAuth 2.1, which requires PKCE. Kick is the first polyemesis provider that uses it.",
			CapStreamKey: "Checked against Kick's published Channels, Livestreams and Users endpoints — none of them return a stream key. This is a documented absence, not a missing feature on our side, and it does not hold back anything else.",
			CapMetadata:  "Stream title, category and up to ten custom tags, over PATCH /public/v1/channels.",
			CapChatRead:  "Kick delivers chat by webhook rather than a socket, so polyemesis needs a public HTTPS URL it can be reached on. Without one the pane is silent, and it warns you rather than letting silence look like a quiet chat.",
			CapModeration: "Delete a message, over moderation:chat_message:manage. Banning and timing out are not " +
				"implemented and the moderation:ban scope is deliberately not requested: nothing in polyemesis bans a " +
				"viewer, and asking a restreamer's audience for that power would be overreach. Use Kick's own " +
				"dashboard.",
			CapViewerStats: "Live state and viewer count from Kick's livestreams endpoints.",
		},
	},
	{
		PresetID: "x", Name: "X (Twitter) Live", Tier: TierManual,
		Summary:   "Paste your ingest URL and stream key. There is no API to connect: X's developer platform covers posts, users, media and the post firehose, not live-video ingest.",
		ReadFirst: "\"Streaming\" in X's API documentation means streaming posts, not ingesting video. No documented third-party live-video ingest endpoint exists, and access to what is documented is credit-based and paid. Set the source up in X's own producer tooling and copy both fields across.",
		Caps: map[Capability]Support{
			CapSSO:       SupportNo,
			CapStreamKey: SupportManual,
			// Everything below hangs off a live broadcast object that the X API
			// does not expose to third parties in the first place.
			CapMetadata:    SupportNo,
			CapChatRead:    SupportNo,
			CapChatSend:    SupportNo,
			CapModeration:  SupportNo,
			CapViewerStats: SupportNo,
		},
		Reasons: map[Capability]string{
			CapSSO: "Nothing to sign into for live video. An OAuth app here would grant access to posts, which is not what a restreamer needs.",
		},
	},
	{
		PresetID: "rumble", Name: "Rumble", Tier: TierManual,
		Summary:   "Paste your ingest URL and stream key from Rumble Studio. Rumble has an API page, but it sits behind a login and nothing about it is published.",
		ReadFirst: "rumble.com/account/api requires an account to view and documents nothing publicly, so polyemesis makes no claim about what it can or cannot do. If you have access and it turns out to offer more, that is a gap in our knowledge rather than a limit of the platform.",
		Caps: map[Capability]Support{
			CapSSO:       SupportUnknown,
			CapStreamKey: SupportManual,
			// Undocumented is not the same as absent. Every one of these is
			// genuinely unknown and is reported that way.
			CapMetadata:    SupportUnknown,
			CapChatRead:    SupportUnknown,
			CapChatSend:    SupportUnknown,
			CapModeration:  SupportUnknown,
			CapViewerStats: SupportUnknown,
		},
	},
	{
		PresetID: "dlive", Name: "DLive", Tier: TierManual,
		Summary:   "Paste your ingest URL and stream key from DLive → Dashboard → Stream settings. Streaming works; there is no integration to connect.",
		ReadFirst: "DLive's developer portal at dev.dlive.tv no longer resolves in DNS, so its developer support appears to be inactive. Nothing about streaming to DLive depends on that — but do not go looking for an API key, because there is currently nowhere to get one.",
		Caps: map[Capability]Support{
			CapSSO:         SupportUnknown,
			CapStreamKey:   SupportManual,
			CapMetadata:    SupportUnknown,
			CapChatRead:    SupportUnknown,
			CapChatSend:    SupportUnknown,
			CapModeration:  SupportUnknown,
			CapViewerStats: SupportUnknown,
		},
	},
	{
		PresetID: "instagram", Name: "Instagram Live", Tier: TierUnsupported,
		Summary:   "polyemesis cannot stream to Instagram. Instagram's platform covers messaging, content publishing and comments — there is no Live broadcast API, and Live Producer's RTMP path was removed for most accounts.",
		ReadFirst: "This entry exists to save you the evening. A destination that silently never connects is worse than no destination at all: it looks like a bug in polyemesis, and there is nothing to fix. If your account still has Live Producer RTMP access, add a Generic RTMPS destination and paste the server URL and key Meta gives you — but check that you have it before you build the show around it.",
		Caps: map[Capability]Support{
			CapSSO: SupportNo,
			// Not SupportManual: for most accounts there is no key to paste,
			// so offering the paste field as the answer would be its own lie.
			CapStreamKey:   SupportNo,
			CapMetadata:    SupportNo,
			CapChatRead:    SupportNo,
			CapChatSend:    SupportNo,
			CapModeration:  SupportNo,
			CapViewerStats: SupportNo,
		},
	},
}

// PlatformCapabilities returns the matrix.
func PlatformCapabilities() []PlatformCapability {
	out := make([]PlatformCapability, len(platformCapabilities))
	copy(out, platformCapabilities)
	return out
}

// CapabilityForPreset returns the row for a destination preset id.
//
// A preset with no row is the common case — the catalogue holds thirty-odd
// entries and only eight have anything to say beyond "paste the key" — so this
// returns a manual-tier row rather than failing. Callers get a usable answer
// for every preset in the catalogue and never have to special-case the absence.
func CapabilityForPreset(id, name string) PlatformCapability {
	for _, p := range platformCapabilities {
		if p.PresetID == id {
			return p
		}
	}
	if name == "" {
		name = id
	}
	return PlatformCapability{
		PresetID: id, Name: name, Tier: TierManual,
		Summary: "Paste the ingest URL and stream key from this platform's dashboard. Every polyemesis feature that happens on this side of the wire — per-destination audio routing, renditions, reconnect, meters — works exactly the same.",
		Caps: map[Capability]Support{
			CapStreamKey: SupportManual,
			// The rest default to SupportUnknown through Get, which is the
			// right answer: we have not looked at this platform's API, and
			// saying "no" about an API we have not read is how a capability
			// check starts refusing things that work.
		},
	}
}

// CapabilityForPlatform returns the row for a platform that has integration
// code behind it. The second result is false for db.PlatformCustom and for
// anything else without a row, which is not an error — a custom destination
// has no platform capabilities to describe.
func CapabilityForPlatform(p db.Platform) (PlatformCapability, bool) {
	if p == "" || p == db.PlatformCustom {
		return PlatformCapability{}, false
	}
	for _, row := range platformCapabilities {
		if row.Platform == p {
			return row, true
		}
	}
	return PlatformCapability{}, false
}

// UnsupportedPresets returns the preset ids polyemesis cannot stream to, so the
// destination picker can mark them without knowing why. The reason travels in
// the row itself.
func UnsupportedPresets() []string {
	var out []string
	for _, p := range platformCapabilities {
		if p.Tier == TierUnsupported {
			out = append(out, p.PresetID)
		}
	}
	return out
}
