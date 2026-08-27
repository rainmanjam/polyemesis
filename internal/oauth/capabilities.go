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
	// CapBroadcastLifecycle is whether polyemesis can tell the PLATFORM to
	// start and stop, as opposed to merely sending it video.
	//
	// IT IS A COLUMN BECAUSE THE ANSWER DIFFERS AND CHANGES WHICH PLATFORM YOU
	// PICK, which is the bar this matrix sets for one. Twitch and Kick publish
	// nothing -- established by enumerating all 149 Helix endpoints and all 27
	// Kick operations, not by failing to find a page -- so on those two the
	// stream IS the trigger and liveness can only be observed. YouTube has a
	// documented state machine, Facebook has an end call, X has both and is
	// unbuilt. An operator choosing where to run an unattended channel is
	// choosing on exactly this.
	CapBroadcastLifecycle Capability = "broadcastLifecycle"
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
	// Kick was the reason this tier existed; it fetches its key now and moved to
	// integrated. The tier was kept empty on the argument that the shape recurs
	// -- "a provider can ship SSO long before it exposes a key endpoint, which
	// is exactly the state Kick was in" -- and Vimeo is the recurrence. Vimeo
	// signs in on every plan and hands over a key on none of them, because its
	// key belongs to a live event and every live method is Enterprise-only.
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
		{CapBroadcastLifecycle, "Start / end", "Tell the platform to go live and to end, rather than only sending it video."},
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
			CapSSO:                SupportYes,
			CapStreamKey:          SupportYes,
			CapMetadata:           SupportYes,
			CapChatRead:           SupportYes,
			CapChatSend:           SupportYes,
			CapModeration:         SupportYes,
			CapViewerStats:        SupportYes,
			CapBroadcastLifecycle: SupportYes,
		},
		Reasons: map[Capability]string{
			CapBroadcastLifecycle: "Goes live on YouTube when video actually starts arriving, and ends when you disable or delete the destination \u2014 never when the encoder merely crashes, because a completed YouTube broadcast cannot return to live and a crash is recoverable. A refused transition raises a fault and never stops the stream.",
			CapViewerStats: "Live state, title, start time and concurrent viewer count, over the same auth/youtube " +
				"scope everything else here uses \u2014 so an account connected before this existed can already " +
				"do it, with no reconnect. It costs two calls: polyemesis stores no video id, so it asks which " +
				"broadcast is live and then asks that video how many people are watching. YouTube omits the " +
				"viewer count when the owner has hidden it, when nobody is watching, and once the broadcast " +
				"ends \u2014 all three look identical, so polyemesis reports \"not reported\" rather than zero. " +
				"The count shares the Data API's daily quota with title push and chat, which is why it is " +
				"polled gently rather than live.",
			CapChatRead: "Polled against the Data API's daily quota, which polyemesis paces. A long broadcast can exhaust it; the chat pane says so with the reset time rather than going quiet.",
			CapModeration: "Delete a message, over the same auth/youtube scope everything else here uses — so an account " +
				"connected before this existed can already do it, with no reconnect. The connected account still has to " +
				"own the broadcast or moderate its chat; YouTube answers 403 otherwise and polyemesis passes that on. " +
				"Banning and timing out work too, over the same scope — permanent, or a timeout in seconds.",
		},
	},
	{
		PresetID: "twitch", Name: "Twitch", Platform: db.PlatformTwitch,
		Tier:    TierIntegrated,
		Summary: "Connect a Twitch account and polyemesis fetches the stream key, sets your title and category at go-live, and joins chat over IRC.",
		HelpURL: "https://dev.twitch.tv/console/apps",
		Caps: map[Capability]Support{
			CapSSO:                SupportYes,
			CapStreamKey:          SupportYes,
			CapMetadata:           SupportYes,
			CapChatRead:           SupportYes,
			CapChatSend:           SupportYes,
			CapModeration:         SupportYes,
			CapViewerStats:        SupportYes,
			CapBroadcastLifecycle: SupportNo,
		},
		Reasons: map[Capability]string{
			CapMetadata: "Title and category, over the channel:manage:broadcast scope.",
			CapViewerStats: "Live state, viewer count, title, category and start time from Helix Get Streams. It needs " +
				"no scope of its own — Twitch asks only for an app or user access token — so every account " +
				"already connected can answer without reconnecting. A channel that is not live returns no " +
				"count at all rather than a count of zero, and polyemesis reports the difference. Twitch " +
				"publishes no encoder health on this endpoint: there is no bitrate, framerate or " +
				"dropped-frame figure to show beside the viewer number.",
			CapModeration: "Delete a message, over moderator:manage:chat_messages. An account connected before this " +
				"existed holds a token without that scope — the account list says so and asks you to reconnect, " +
				"rather than letting the delete button fail on the message you needed gone. Twitch refuses to delete " +
				"anything older than six hours, and refuses the broadcaster's own messages and other moderators'. " +
				"Banning and timing out work over moderator:manage:banned_users, which is a separate scope from " +
				"deletion because removing a person is a bigger ask than removing a message.",
		},
	},
	{
		PresetID: "facebook", Name: "Facebook Live", Platform: db.PlatformFacebook,
		Tier:    TierIntegrated,
		Summary: "Connect a Facebook profile or Page and polyemesis creates the broadcast, splits out the RTMPS ingest and key, pushes the title and description, and reads the comment thread.",
		// Up front, not in step six. Meta's review is measured in days and it
		// is the operator's to do — a setup guide that mentions it late has
		// already wasted their evening.
		ReadFirst: "Meta requires App Review before anyone other than you can connect an account. Your own account works immediately as a developer or tester of your app, which is all a single-operator setup needs — but publishing on someone else's behalf needs Advanced Access to publish_video (profiles) or pages_manage_posts plus pages_read_engagement (Pages). Budget days, not minutes, and start it before you need it. Separately, and NOT a permission: Meta refuses go-live on account properties no scope can satisfy. Since 2024-06-10 the account must be at least 60 days old and the Page or professional-mode profile must have at least 100 followers. A connection with every scope granted still fails both, and the Graph error names neither \u2014 so if everything looks correct and it still will not start, check those two first.",
		HelpURL:   "https://developers.facebook.com/apps",
		Caps: map[Capability]Support{
			CapSSO:       SupportYes,
			CapStreamKey: SupportYes,
			CapMetadata:  SupportYes,
			CapChatRead:  SupportYes,
			// Refused in Facebook's own words rather than merely absent: the
			// live-video comments edge has a "Creating" section whose entire
			// content is "You can't perform this operation on this endpoint."
			// A sweep of all five readable LiveVideo edge references finds no
			// comment POST. The generic /{object-id}/comments page lists Live
			// Video among its nodes, but it never writes that path and a
			// page-level implication does not outrank a per-endpoint refusal.
			CapChatSend:           SupportNo,
			CapModeration:         SupportYes,
			CapViewerStats:        SupportUnknown,
			CapBroadcastLifecycle: SupportYes,
		},
		Reasons: map[Capability]string{
			// WHY THIS CELL NEEDS A SENTENCE AND DID NOT HAVE ONE. Facebook and
			// YouTube both read "Works" here, and they mean different things.
			// YouTube is DRIVEN: the lifecycle coordinator transitions it to live
			// and ends it on its own. Facebook is COMMANDED BY HAND -- connecting
			// the account creates the live video, and ending it is a menu item the
			// operator presses. Both are real, so SupportYes is right for both;
			// but an operator comparing the two cells with only YouTube's sentence
			// in front of them would reasonably read Facebook's bare Works as the
			// same automation, and then leave an unattended channel expecting an
			// end that nothing is going to issue.
			CapBroadcastLifecycle: "Creating the broadcast and ending it both work, but they are things YOU do — connecting the account creates the live video, and \"End broadcast\" is on the destination menu. Nothing ends a Facebook broadcast on its own, unlike YouTube.",
			CapStreamKey:          "Facebook issues a fresh ingest and key per broadcast, so connecting the account is what creates the broadcast. There is no permanent key to reuse.",
			CapModeration: "Delete a comment, or HIDE one — Facebook is the only platform here that can take a message off " +
				"the public thread without destroying it, because its live chat is a comment thread. Acting on a " +
				"Page's comments needs the MODERATE task permission, which is separate from being able to read them, " +
				"so an app that shows you the thread can still be refused when you act on it.",
			CapMetadata: "Title and description. Facebook removed overlay_url in Graph API v24.0, so there is no overlay field to push.",
			CapChatRead: "Facebook's live chat is the comment thread on the live video, read over the Graph API. A destination whose key was pasted by hand has no live-video id to attach to, and the chat pane says so.",
		},
	},
	{
		PresetID: "kick", Name: "Kick", Platform: db.PlatformKick,
		// INTEGRATED, not partial. Kick answers SupportYes on all seven
		// capabilities -- one more than YouTube or Twitch, both of which are
		// integrated -- and the key is fetched over streamkey:read rather than
		// pasted, which is the only thing the partial tier means. The row said
		// partial for as long as the key really was manual and was not moved
		// when that changed, so the UI badge told an operator to paste a key
		// polyemesis fetches for them. db/platforms.go carries a comment asking
		// for exactly this to be kept in step.
		Tier:    TierIntegrated,
		Summary: "Sign in with Kick and polyemesis fetches the ingest URL and stream key, sets the title, category and tags, reads and replies to chat, and reads viewer stats.",
		HelpURL: "https://kick.com/dashboard/settings/stream",
		Caps: map[Capability]Support{
			CapSSO:                SupportYes,
			CapStreamKey:          SupportYes,
			CapMetadata:           SupportYes,
			CapChatRead:           SupportYes,
			CapChatSend:           SupportYes,
			CapModeration:         SupportYes,
			CapViewerStats:        SupportYes,
			CapBroadcastLifecycle: SupportNo,
		},
		Reasons: map[Capability]string{
			CapSSO: "OAuth 2.1, which requires PKCE. Kick is the first polyemesis provider that uses it.",
			CapStreamKey: "Fetched from the channels resource, over the streamkey:read scope. " +
				"This was recorded here as impossible for a long time, and the reasoning is worth keeping: " +
				"Kick publishes no /streamkey endpoint, so reading the endpoint list finds nothing — " +
				"the key rides as stream.key on the same channels response we already fetch, and is " +
				"withheld unless streamkey:read was granted, which the Get Channels page does not list " +
				"among its required scopes. An account connected before that scope was requested must be " +
				"reconnected once.",
			CapMetadata: "Stream title, category and up to ten custom tags, over PATCH /public/v1/channels.",
			CapChatRead: "Kick delivers chat by webhook rather than a socket, so polyemesis needs a public HTTPS URL it can be reached on. Without one the pane is silent, and it warns you rather than letting silence look like a quiet chat.",
			CapModeration: "Delete a message, over moderation:chat_message:manage. Banning and timing out work over " +
				"moderation:ban. Note that Kick counts timeouts in MINUTES where YouTube and Twitch count seconds, " +
				"and caps them at 7 days; polyemesis converts, so you give it one unit everywhere.",
			CapViewerStats: "Live state and viewer count from Kick's livestreams endpoints.",
		},
	},
	{
		PresetID: "trovo", Name: "Trovo", Platform: db.PlatformTrovo,
		// INTEGRATED even though one field of the ingest is still typed. The
		// partial tier means the KEY is pasted, and it is not: it is fetched
		// over channel_details_self like YouTube's and Twitch's. What Trovo
		// does not publish is the ingest HOSTNAME, which is a different field
		// and a one-time copy rather than a per-broadcast chore.
		Tier:    TierIntegrated,
		Summary: "Sign in with Trovo and polyemesis fetches the stream key, sets the title and category at go-live, and reads the live viewer count. The server URL is the one field you copy across yourself, once.",
		// This row was eight cells of Unverified with a note saying the API was
		// answering and nothing had been built against it. It has been built
		// against now, from Trovo's own reference read 2026-08-26 and recorded
		// in docs/evidence/vimeo-trovo-oauth-2026-08-26.md.
		ReadFirst: "Trovo does not hand out the client secret from its developer portal. Its documentation says, verbatim, “If you don't have the Client Secret, please contact: developer@trovo.live” — and the authorization-code flow cannot complete without one, because Trovo documents no PKCE. Ask for it before the day you need it. Nothing else here is gated: registration is open and every capability below is in the published reference.",
		HelpURL:   "https://developer.trovo.live/docs/APIs.html",
		Caps: map[Capability]Support{
			CapSSO:       SupportYes,
			CapStreamKey: SupportYes,
			CapMetadata:  SupportYes,
			// DOCUMENTED AND UNBUILT, which is the same expressive gap the X row
			// records: none of the four Support values says "the platform
			// publishes this and polyemesis has not written it yet". Unverified
			// is the least wrong — it renders as an invitation to try rather
			// than as a refusal — and the Reasons below name the endpoint and
			// the scope so the next person starts from evidence rather than
			// from a search. Do NOT read these three as absent APIs.
			CapChatRead:    SupportUnknown,
			CapChatSend:    SupportUnknown,
			CapModeration:  SupportUnknown,
			CapViewerStats: SupportYes,
			// Checked, not assumed: the whole APIs reference was read end to end
			// and there is no broadcast object and no transition call anywhere
			// in it. Same answer as Twitch and Kick, reached the same way.
			CapBroadcastLifecycle: SupportNo,
		},
		Reasons: map[Capability]string{
			CapSSO: "OAuth 2.0 authorization code with a client_secret. Trovo documents no PKCE, so polyemesis does not send one — and the secret arrives by email from developer@trovo.live rather than from the portal.",
			CapStreamKey: "Fetched from GET /openplatform/channel over the channel_details_self scope. " +
				"THE SERVER URL IS NOT: Trovo issues the ingest hostname per region and publishes it " +
				"nowhere in its API, so that one field is copied from the creator dashboard once and " +
				"refreshing the key afterwards leaves it alone. An account connected before this " +
				"integration existed has to be reconnected once.",
			CapMetadata: "Title and category, over POST /openplatform/channels/update with channel_update_self. " +
				"Trovo's channel update takes no description and has no tags, so those are reported as " +
				"skipped rather than silently dropped.",
			CapChatRead:           "Trovo delivers chat over a websocket (developer.trovo.live's Chat Service doc) rather than by poll or webhook. Real, documented, and not wired up here yet.",
			CapChatSend:           "POST /openplatform/chat/send, over chat_send_self plus send_to_my_channel. Not wired up here yet, and the scopes are deliberately not requested until it is — asking for them early would put permissions on the consent screen that nothing uses.",
			CapModeration:         "POST /openplatform/channels/command with manage_messages, which performs Trovo's own chat commands — “ban xxx”, “mod @xxx”, with no leading slash. Not wired up here yet; the scope is not requested until it is.",
			CapViewerStats:        "Live state and viewer count from the same channel read the stream key comes from, so it costs no extra call and no extra scope. It reads current_viewers rather than Trovo's dedicated viewers endpoint on purpose: that endpoint's total is documented as the channel's “total login users”, which counts signed-in viewers only and would under-report an audience without saying so. Offline, polyemesis reports no count rather than zero.",
			CapBroadcastLifecycle: "Trovo has no broadcast resource: nothing creates, starts or ends one, so the stream itself is the trigger and liveness can only be observed. Established by reading the whole APIs reference, not by failing to find a page.",
		},
	},
	{
		PresetID: "vimeo", Name: "Vimeo Livestream", Platform: db.PlatformVimeo,
		// THE FIRST ROW IN THIS TIER SINCE KICK LEFT IT, and the tier's own
		// comment predicted exactly this shape: "a provider can ship SSO long
		// before it exposes a key endpoint". Vimeo signs in on any plan and
		// hands over no key on any plan, so partial is not a compromise between
		// integrated and manual -- it is the accurate description.
		Tier:    TierPartial,
		Summary: "Sign in with Vimeo and polyemesis reads which member the token belongs to and checks, at the moment you connect, whether your account can reach Vimeo's live API. The ingest URL and stream key are pasted from the live event: Vimeo issues them per event, and creating one is Enterprise-only.",
		// THE WHOLE ROW HANGS OFF ONE SENTENCE OF VIMEO'S, so it is quoted
		// first and quoted exactly. An operator who reads this before setting
		// anything up has been told the only thing that decides whether the
		// rest is worth their evening -- which is what ReadFirst is for, and
		// why Facebook's App Review note lives in the same field.
		ReadFirst: "Read this first: \"our live API is available only to Vimeo Enterprise customers\" — Vimeo's own words on its live API reference, read 2026-08-26. That is a commercial gate, not a permission: no scope, no reconnection and no app setting lifts it, and it applies to every live method Vimeo publishes (create an event, activate it, end it, read its ingest, its M3U8 playback and its thumbnails). Sign-in itself is open to any Vimeo plan, and polyemesis asks the live API whether YOUR account reaches it the moment you connect, so you find out then rather than mid-broadcast. Streaming to Vimeo works regardless — you paste the RTMPS URL and key from the event's setup panel, exactly as before. Vimeo is also deprecating one-time live events and recommends avoiding them, so create a recurring event.",
		HelpURL:   "https://developer.vimeo.com/api/reference/live",
		Caps: map[Capability]Support{
			CapSSO: SupportYes,
			// By hand, and it stays by hand even for an Enterprise account
			// today: polyemesis does not create Vimeo events, and a key belongs
			// to an event. Ingest says which of those two reasons applies to
			// the account in front of it rather than assuming the common one.
			CapStreamKey: SupportManual,
			// EVERY CELL BELOW IS "Unverified" AND NOT ONE OF THEM MEANS WHAT
			// THE LEGEND SAYS IT MEANS, which is a finding about the vocabulary
			// rather than about Vimeo.
			//
			// SupportUnknown renders as "Unverified": "Not built yet, and the
			// platform's API was not confirmed either way." The first half is
			// true. The second is FALSE for metadata and for start/end -- both
			// were confirmed, from Vimeo's own live reference, and the Reasons
			// entries below name the methods. The X row above records the same
			// gap in its own words ("None of the four Support values says 'the
			// platform documents it and we have not built it'"); Vimeo adds a
			// second axis to it, because even a built lifecycle would be
			// unreachable for the median reader of this table.
			//
			// SupportUnknown is still the least wrong of the four. "Works"
			// would be a promise no code keeps; "By hand" describes a step the
			// operator can take, and there is none; "Not possible" would be a
			// refusal contradicted by Vimeo's own published reference, and
			// this file's sourcing rule makes a wrong SupportNo the most
			// expensive mistake available here. Unknown is the fail-open one
			// and it invites the operator to try. The accompanying report
			// proposes the fifth value rather than forcing one of the four to
			// stretch.
			CapMetadata:           SupportUnknown,
			CapChatRead:           SupportUnknown,
			CapChatSend:           SupportUnknown,
			CapModeration:         SupportUnknown,
			CapViewerStats:        SupportUnknown,
			CapBroadcastLifecycle: SupportUnknown,
		},
		Reasons: map[Capability]string{
			CapSSO:                "OAuth 2.0 authorization code against api.vimeo.com, over the public and private scopes. Vimeo can also verify your client ID and secret before you connect anything, so a typo is caught on the credentials page. PKCE is not documented for Vimeo and is therefore not sent — an authorization server that validates its query string strictly refuses an unknown parameter outright.",
			CapStreamKey:          "Vimeo has no permanent stream key: the ingest URL and key belong to a live event, and creating one is behind the Enterprise gate. So this is a paste, from the event's setup panel. polyemesis asks the live API which reason applies to your account and says so rather than assuming.",
			CapMetadata:           "Vimeo publishes \"Update an event\", and it is one of the methods the Enterprise gate covers. Nothing here calls it, so this is not built — but it is not unknown either, and the Unverified label overstates the doubt.",
			CapBroadcastLifecycle: "Vimeo publishes \"Activate an event\" and \"End an event\", so the lifecycle polyemesis models maps cleanly onto it. Both are Enterprise-only and neither is wired up, so nothing here starts or ends a Vimeo broadcast today. Note also that Vimeo is deprecating one-time live events and recommends avoiding them; anything built here should target recurring events.",
			CapViewerStats:        "Vimeo publishes a VPaaS viewer analytics EXPORT on live events, which is not the same thing as a live concurrent count, and it sits behind the same Enterprise gate. Whether a live count is readable at all is genuinely unchecked, so this cell means what the legend says.",
		},
	},
	{
		PresetID: "x", Name: "X (Twitter) Live", Tier: TierManual,
		Summary:   "Paste your ingest URL and stream key. X does publish a live-video API \u2014 broadcasts, chat and moderation \u2014 and polyemesis has not wired it up yet, so for now this is a paste-the-key destination that streams exactly as well as any other.",
		ReadFirst: "X's live-video API is real but its access tier is not published. Every endpoint below is in X's own served OpenAPI spec, and no pricing or tier page names the Broadcasts family -- so whether your account can call them at all is a question only a live request answers. Get the stream key from X's producer tooling and paste it; everything else is automatic once you connect.",
		Caps: map[Capability]Support{
			// THIS ROW WAS SEVEN SupportNo CELLS AND ALMOST ALL OF THEM WERE
			// WRONG. The summary used to say "There is no API to connect: X's
			// developer platform covers posts, users, media and the post
			// firehose, not live-video ingest", and a comment here claimed
			// everything below "hangs off a live broadcast object that the X
			// API does not expose to third parties in the first place".
			// GET /2/broadcasts/{id} is that object.
			//
			// Established by counting, not by reading: api.x.com/2/openapi.json
			// (856,423 bytes, "X API v2" 2.167) declares 149 paths and 178
			// operations, of which the Broadcasts tag holds 13 and Chat 16,
			// under the scopes broadcast.read and broadcast.write. Nine paths
			// carry the word broadcast, including POST
			// /2/broadcasts/scheduled/{id}/live and GET+POST
			// /2/broadcasts/{id}/chat. See docs/evidence/facebook-chat-rumble-x-2026-08-16.md.
			// EVERY CELL BELOW WAS SupportYes FOR ABOUT AN HOUR, AND THAT WAS A
			// PROMISE THE UI COULD NOT KEEP. X's API genuinely publishes all of
			// it -- see docs/evidence/facebook-chat-rumble-x-2026-08-16.md, and
			// the OpenAPI counts in this row's history -- but the provider is
			// not registered, there is no connect affordance, and an operator
			// reading "Works" would go looking for a sign-in button that does
			// not exist.
			//
			// The house rule is the one Rumble's viewer-stats cell already
			// states: a capability nothing implements is not a capability. It
			// was applied there and broken here in the same commit.
			//
			// None of the four Support values says "the platform documents it
			// and we have not built it" -- the same expressive gap that got
			// Twitch predictions cut. SupportUnknown is the least wrong: it
			// renders as Unverified, which is fail-open and invites the
			// operator to try, rather than as a refusal or a lie.
			CapSSO: SupportUnknown,
			// Still pasted, and the reason is narrower than it looks: X
			// CONSUMES a key (source_id is required at create) and echoes it
			// back on every broadcast object, but publishes nothing that mints
			// or enumerates one. So polyemesis can verify a binding it was
			// given; it cannot obtain one.
			CapStreamKey:          SupportManual,
			CapMetadata:           SupportUnknown,
			CapChatRead:           SupportUnknown,
			CapChatSend:           SupportUnknown,
			CapModeration:         SupportUnknown,
			CapViewerStats:        SupportUnknown,
			CapBroadcastLifecycle: SupportUnknown,
		},
		Reasons: map[Capability]string{
			CapSSO:         "OAuth 2.0 authorization code at api.x.com/2/oauth2/authorize with the broadcast.read and broadcast.write scopes, declared in X's served OpenAPI spec. PKCE is X's documented practice elsewhere but is not stated in the spec for this flow.",
			CapStreamKey:   "X consumes a stream key and hands it back -- source_id is required when the broadcast is created and is read back on every broadcast object -- but nothing mints or lists one. Paste it once from X's producer tooling; polyemesis can then confirm the binding rather than trust its stored copy.",
			CapMetadata:    "Title, description, language and the chat option are set on a SCHEDULED broadcast, over POST and PUT /2/broadcasts/scheduled. There is no metadata update on an already-live broadcast, so a title change mid-show is not possible here.",
			CapChatRead:    "Chat history over GET /2/broadcasts/{id}/chat with broadcast.read. Real-time push needs two separate auth objects -- a user-context subscription for the broadcast.chat event, plus an app-only bearer token on the activity stream.",
			CapChatSend:    "POST /2/broadcasts/{id}/chat with broadcast.read and broadcast.write. Messages are limited to 140 characters, which is X's own bound and not ours.",
			CapModeration:  "Mute a viewer, lift the mute, and delete a message, over /2/broadcasts/{id}/chat/mutes and chat/{message_id}. X publishes no error taxonomy for any of the three -- every Broadcasts operation declares only a success code and a generic default -- so a refusal arrives without a reason to show you.",
			CapViewerStats: "total_watching and total_watched are on the broadcast object, and X documents NOTHING about what they mean. Every one of the 26 fields in that schema is an undescribed string, so the numbers are readable but their unit, freshness and whether either counts unique people are all unstated. A viewer count shown to an operator asserts a meaning, so this stays unverified until X documents them or a live broadcast calibrates them.",
		},
	},
	{
		PresetID: "rumble", Name: "Rumble", Platform: db.PlatformRumble,
		Tier:      TierManual,
		Summary:   "Paste your ingest URL and stream key from Rumble Studio. Chat is different: Rumble's live-stream API hands over the chat with a key from your own account settings, so the pane works without any sign-in.",
		ReadFirst: "The chat key is NOT the stream key and is not pasted into polyemesis's UI. It comes from rumble.com/account/livestream-api and is supplied in the RUMBLE_CHAT_API_KEY environment variable, because there is no account to store it against — this API has no sign-in. Treat that URL as a secret: it is the whole credential, and anyone holding it can read your chat.",
		HelpURL:   "https://rumble.com/account/livestream-api",
		Caps: map[Capability]Support{
			// Unchanged. Note that the live-stream API response DOES carry
			// livestreams[].stream_key, so a key fetch may well be possible —
			// but nothing here reads that field, deliberately, and a capability
			// nothing implements is not a capability. Recorded rather than
			// quietly claimed.
			CapStreamKey: SupportManual,
			CapMetadata:  SupportManual,
			CapChatRead:  SupportYes,
			// THESE WERE SupportUnknown AND THE ARGUMENT FOR THAT WAS GOOD.
			// It ran: "'I looked and did not find it' on an API this thinly
			// documented is not the same as reading a published spec and
			// finding the thing absent. A wrong SupportNo here would become a
			// refusal an operator cannot argue with, on a platform whose
			// surface we have barely seen."
			//
			// That bar is now met, and by affirmative evidence rather than by
			// more looking. Rumble's complete 158-article knowledge base was
			// enumerated. Its single API article states, published and in
			// Rumble's own words, "Authentication is not required for this
			// version of the API"; rumble.com/oauth/authorize answers an
			// HONEST 404, which is a probe rather than a search; and the
			// moderation article describes the mechanism as "Select the three
			// dots next to a message in live chat" and contains the word API
			// zero times. The published API is one read-only snapshot GET, so
			// the surface is not barely seen -- it is small and fully read.
			//
			// See docs/evidence/facebook-chat-rumble-x-2026-08-16.md.
			CapSSO:        SupportNo,
			CapChatSend:   SupportNo,
			CapModeration: SupportNo,
			// Documented but unread, and it stays unverified for exactly that
			// reason: watching_now sits in the same get-data response the chat
			// poller already fetches, so this is a field read away rather than
			// an integration away. The stats drift test is a biconditional --
			// a SupportYes here with no Stats method would fail it, correctly.
			CapViewerStats:        SupportUnknown,
			CapBroadcastLifecycle: SupportNo,
		},
		Reasons: map[Capability]string{
			CapChatRead: "Polled from rumble.com/-livestream-api/get-data, which needs no sign-in — the key from your " +
				"account settings is the whole credential. Chat only exists while you are live, and the pane says it " +
				"is waiting rather than going quiet. Two caveats worth knowing before you rely on it: Rumble sends " +
				"no message id, so polyemesis derives one from the content, and two identical messages from one " +
				"person inside the same second are indistinguishable and show as one. And Rumble publishes no rate " +
				"limit, so the poll is deliberately conservative at ten seconds rather than as fast as it could be.",
			CapStreamKey:  "Rumble Studio issues both fields; copy them across by hand. There is a Static Stream Key that does not change between broadcasts, which makes this a one-time paste rather than a per-stream chore -- but it is obtainable only through Rumble's own settings UI, never over the API.",
			CapSSO:        "Rumble publishes no OAuth and no developer sign-in. Its one API article says authentication is not required for that API, and rumble.com/oauth/authorize is a genuine 404 -- only an unpublished partner agreement could change this.",
			CapMetadata:   "No write API exists. Rumble's own mechanism is a live-stream template chosen in your account settings, which it applies automatically each time the static key goes live -- so set the title and category there, before the broadcast, rather than expecting polyemesis to push them.",
			CapChatSend:   "The entire published API is one read-only snapshot request; no send path, method or request body is documented anywhere. Chat bots that appear to work drive Rumble's private frontend with a stored account password, which polyemesis will not do.",
			CapModeration: "Rumble documents moderation as a UI action -- three dots next to a message in live chat -- and its moderation article does not mention an API at all.",
		},
	},
	manualUnverified(
		"dlive", "DLive",
		"Paste your ingest URL and stream key from DLive → Dashboard → Stream settings. Streaming works; there is no integration to connect.",
		"DLive's developer portal at dev.dlive.tv no longer resolves in DNS, so its developer support appears to be inactive. Nothing about streaming to DLive depends on that — but do not go looking for an API key, because there is currently nowhere to get one.",
	),
	manualUnverified(
		"odysee", "Odysee",
		"Paste your ingest URL and stream key from Odysee. Streaming works; there is no integration to connect.",
		"Odysee's chat is the LBRY comment server, and both comments.odysee.com and comments.lbry.com answered 502 when last checked. A 502 is an outage rather than a removal, so this is unverified rather than unsupported -- but there is nothing to build against while it stays that way.",
	),
	manualUnverified(
		"dailymotion", "Dailymotion",
		"Paste your ingest URL and stream key from Dailymotion. Streaming works; there is no integration to connect.",
		"api.dailymotion.com is live and openly readable. Whether it exposes live-stream chat has not been checked, so every column below is genuinely unknown rather than known to be absent.",
	),
	manualUnverified(
		"tiktok", "TikTok LIVE",
		"Paste the server URL and key TikTok issues for the broadcast. Streaming works; there is no integration to connect.",
		"TikTok's developer APIs are real and answering — open.tiktokapis.com returns a structured auth error rather than a 404 — but the live surface is gated behind a partner programme, not open registration. Nothing here is reachable by pasting a token from a developer console.",
	),
	manualUnverified(
		"linkedin", "LinkedIn Live",
		"Paste the ingest URL and key LinkedIn issues for the event. Streaming works; there is no integration to connect.",
		"LinkedIn Live requires approved broadcast-partner status, and its APIs sit behind the Marketing Developer Platform rather than open registration. api.linkedin.com answers, so the surface exists — but access to it is granted, not requested.",
	),
	{
		PresetID: "instagram", Name: "Instagram Live", Tier: TierUnsupported,
		Summary:   "polyemesis cannot automate Instagram: there is no Live broadcast API, so nothing can create a broadcast, fetch a key, read chat or report viewers. If your account still has Live Producer, its URL and key work as a Generic RTMPS destination — copied by hand, and they change every broadcast.",
		ReadFirst: "This entry exists to save you the evening. A destination that silently never connects is worse than no destination at all: it looks like a bug in polyemesis, and there is nothing to fix. If your account still has Live Producer RTMP access, add a Generic RTMPS destination and paste the server URL and key Meta gives you — but check that you have it before you build the show around it.",
		Caps: map[Capability]Support{
			CapSSO: SupportNo,
			// Not SupportManual: for most accounts there is no key to paste,
			// so offering the paste field as the answer would be its own lie.
			CapStreamKey:          SupportNo,
			CapMetadata:           SupportNo,
			CapChatRead:           SupportNo,
			CapChatSend:           SupportNo,
			CapModeration:         SupportNo,
			CapViewerStats:        SupportNo,
			CapBroadcastLifecycle: SupportNo,
		},
	},
}

// manualUnverified builds the row shared by every platform polyemesis can
// stream to and has not integrated: paste the key, and every other column is
// genuinely unverified.
//
// A function rather than eight copies of the same map, because the copies were
// the point of confusion -- an eight-line block repeated eight times invites
// the reader to diff them looking for the difference, and there isn't one. The
// difference between these platforms lives entirely in ReadFirst, which is
// where it belongs and where it is worth reading.
//
// SupportUnknown, not SupportNo, throughout. See the sourcing rule at the top
// of this file: an unverified capability is reported as unverified and tried
// anyway. Nothing here is a refusal.
func manualUnverified(presetID, name, summary, readFirst string) PlatformCapability {
	return PlatformCapability{
		PresetID:  presetID,
		Name:      name,
		Tier:      TierManual,
		Summary:   summary,
		ReadFirst: readFirst,
		Caps: map[Capability]Support{
			CapSSO:         SupportUnknown,
			CapStreamKey:   SupportManual,
			CapMetadata:    SupportUnknown,
			CapChatRead:    SupportUnknown,
			CapChatSend:    SupportUnknown,
			CapModeration:  SupportUnknown,
			CapViewerStats: SupportUnknown,
			// Unknown rather than No: nobody has read these platforms' APIs
			// for a lifecycle call, and this matrix's own rule is that a No
			// must trace to something checked.
			CapBroadcastLifecycle: SupportUnknown,
		},
	}
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
