/* ===========================================================================
   The platform capability matrix: what a user actually gets from each platform,
   said out loud before they invest an hour in setup.

   Lived in DestinationDialog.tsx, but the settings page renders the same matrix
   as a reference table and imports six of these symbols. Two consumers and no
   JSX is the definition of something that is data rather than part of a dialog.

   Splitting it out also lets the dialog hot-swap: a file exporting a component
   next to a table cannot be, so correcting one platform's "unverified" used to
   full-reload and discard a half-filled destination form.
   =========================================================================== */

/* ------------------------------------------------- platform capability matrix
 *
 *  What a user actually gets from each platform, said out loud before they
 *  invest an hour in setup. Mirrors internal/oauth/capabilities.go, which is
 *  also served from GET /api/v1/platforms/capabilities for scripted clients —
 *  the same arrangement the preset catalogue above already uses.
 *
 *  This is a description, never a gate. Nothing below is consulted to decide
 *  whether a save is allowed: an operator whose account can do something ours
 *  could not verify must be able to try it and read the platform's own error.
 *  See "unknown" below.
 */

export type Capability =
  | "sso"
  | "streamKey"
  | "metadata"
  | "chatRead"
  | "chatSend"
  | "moderation"
  | "viewerStats"
  | "broadcastLifecycle";

/** Four values rather than a boolean, because the interesting platforms are
 *  not binary. Kick's stream key is not "unsupported" — it works perfectly,
 *  the operator just types it. */
export type Support =
  /** polyemesis does this for you today. */
  | "yes"
  /** Works, with one step you do by hand. A pasted key is a supported
   *  destination, not a degraded one. */
  | "manual"
  /** The platform publishes no API for this. Only ever set where somebody
   *  actually read the platform's docs and found the thing absent. */
  | "no"
  /** Not built, and the platform's API not confirmed either way. The honest
   *  default and the fail-open one: shown as unverified, never as a refusal. */
  | "unknown";

export type CapTier = "integrated" | "partial" | "manual" | "unsupported";

export interface PlatformCapability {
  /** Joins this row to an entry in PRESETS. */
  presetId: string;
  name: string;
  /** The platform id for /oauth/{id}/start and for matching connected
   *  accounts. Deliberately a plain string rather than `Platform`: Facebook
   *  signs in and fetches keys today, but the destination row's platform
   *  column only widens to "facebook" once ui/src/lib/types.ts does. Keying
   *  the connect affordance off this means the button works either way. */
  connect?: string;
  tier: CapTier;
  summary: string;
  /** The thing that costs a day if it is met halfway through instead of at
   *  the start. Facebook's App Review is why this field exists. */
  readFirst?: string;
  caps: Partial<Record<Capability, Support>>;
  /** Only the cells where the value alone would raise a question. A wall of
   *  tooltips is not honesty. */
  reasons?: Partial<Record<Capability, string>>;
}

export const CAPABILITY_COLUMNS: {
  key: Capability;
  label: string;
  help: string;
}[] = [
  {
    key: "sso",
    label: "Sign in",
    help: "Connect the account with OAuth instead of pasting secrets.",
  },
  {
    key: "streamKey",
    label: "Stream key",
    help: "polyemesis fetches the ingest URL and key for you.",
  },
  {
    key: "metadata",
    label: "Metadata",
    help: "Set the title, description or category when you go live.",
  },
  {
    key: "chatRead",
    label: "Chat read",
    help: "Messages appear in the unified chat pane.",
  },
  {
    key: "chatSend",
    label: "Chat send",
    help: "You can reply from the chat pane.",
  },
  {
    key: "moderation",
    label: "Moderation",
    help: "Delete a message or time a viewer out.",
  },
  {
    key: "viewerStats",
    label: "Viewers",
    help: "Live viewer count read back from the platform.",
  },
  {
    key: "broadcastLifecycle",
    label: "Start / end",
    help: "Tell the platform to go live and to end, rather than only sending it video.",
  },
];

export const SUPPORT_LEGEND: {
  key: Support;
  label: string;
  help: string;
  variant: "live" | "default" | "outline" | "warn";
}[] = [
  {
    key: "yes",
    label: "Works",
    help: "polyemesis does this for you today.",
    variant: "live",
  },
  {
    key: "manual",
    label: "By hand",
    help: "Supported, with one step you do yourself — usually pasting a key.",
    variant: "default",
  },
  {
    key: "unknown",
    label: "Unverified",
    help: "Not built yet, and the platform's API was not confirmed either way. Nothing stops you trying.",
    variant: "outline",
  },
  {
    key: "no",
    label: "Not possible",
    help: "The platform publishes no API for this, so no amount of setup will produce it.",
    variant: "warn",
  },
];

export const TIER_LEGEND: { key: CapTier; label: string; help: string }[] = [
  {
    key: "integrated",
    label: "Fully integrated",
    help: "Sign in once and polyemesis fetches the ingest URL and stream key.",
  },
  {
    key: "partial",
    label: "Sign in + paste key",
    help: "Sign-in works and brings chat and metadata with it, but the key is typed by hand.",
  },
  {
    key: "manual",
    label: "Manual key",
    help: "Paste the ingest URL and stream key from the platform's dashboard. Streaming works exactly as well; there is just nothing to connect.",
  },
  {
    key: "unsupported",
    label: "Not supported",
    help: "polyemesis cannot stream here. Shown so you do not spend an evening finding that out.",
  },
];

/** Display order: most integrated first, unsupported last — because the last
 *  row is the one nobody should have to scroll to find. */
/** The row shared by every platform polyemesis can stream to and has not
 *  integrated: paste the key, and every other column is genuinely unverified.
 *
 *  A function rather than eight copies of one object. The copies were the
 *  confusion — an identical eight-line block repeated invites the reader to
 *  diff them looking for the difference, and there isn't one. What actually
 *  differs between these platforms is readFirst, which is where it belongs.
 *
 *  Mirrors manualUnverified() in internal/oauth/capabilities.go. */
function manualUnverified(
  presetId: string,
  name: string,
  summary: string,
  readFirst: string,
): PlatformCapability {
  return {
    presetId,
    name,
    tier: "manual",
    summary,
    readFirst,
    caps: {
      sso: "unknown",
      streamKey: "manual",
      metadata: "unknown",
      chatRead: "unknown",
      chatSend: "unknown",
      moderation: "unknown",
      viewerStats: "unknown",
      broadcastLifecycle: "unknown",
    },
  };
}

export const PLATFORM_CAPABILITIES: PlatformCapability[] = [
  {
    presetId: "youtube",
    name: "YouTube Live",
    connect: "youtube",
    tier: "integrated",
    summary:
      "Connect a Google account and polyemesis fetches the ingest URL and stream key, pushes your title and description at go-live, and reads and replies to live chat.",
    caps: {
      sso: "yes",
      streamKey: "yes",
      metadata: "yes",
      chatRead: "yes",
      chatSend: "yes",
      moderation: "yes",
      viewerStats: "yes",
      broadcastLifecycle: "yes",
    },
    reasons: {
      broadcastLifecycle:
        "Goes live on YouTube when video actually starts arriving, and ends when you disable or delete the destination — never when the encoder merely crashes, because a completed YouTube broadcast cannot return to live and a crash is recoverable. A refused transition raises a fault and never stops the stream.",
      viewerStats:
        'Live state, title, start time and concurrent viewer count, over the same auth/youtube scope everything else here uses — so an account connected before this existed can already do it, with no reconnect. It costs two calls: polyemesis stores no video id, so it asks which broadcast is live and then asks that video how many people are watching. YouTube omits the viewer count when the owner has hidden it, when nobody is watching, and once the broadcast ends — all three look identical, so polyemesis reports "not reported" rather than zero. The count shares the Data API\'s daily quota with title push and chat, which is why it is polled gently rather than live.',
      chatRead:
        "Polled against the Data API's daily quota, which polyemesis paces. A long broadcast can exhaust it; the chat pane says so with the reset time rather than going quiet.",
      moderation:
        "Delete a message, over the same auth/youtube scope everything else here uses — so an account connected before this existed can already do it, with no reconnect. The connected account still has to own the broadcast or moderate its chat; YouTube answers 403 otherwise and polyemesis passes that on. Banning and timing out work too, over the same scope — permanent, or a timeout in seconds.",
    },
  },
  {
    presetId: "twitch",
    name: "Twitch",
    connect: "twitch",
    tier: "integrated",
    summary:
      "Connect a Twitch account and polyemesis fetches the stream key, sets your title and category at go-live, and joins chat over IRC.",
    caps: {
      sso: "yes",
      streamKey: "yes",
      metadata: "yes",
      chatRead: "yes",
      chatSend: "yes",
      moderation: "yes",
      viewerStats: "yes",
      broadcastLifecycle: "no",
    },
    reasons: {
      metadata: "Title and category, over the channel:manage:broadcast scope.",
      viewerStats:
        "Live state, viewer count, title, category and start time from Helix Get Streams. It needs no scope of its own — Twitch asks only for an app or user access token — so every account already connected can answer without reconnecting. A channel that is not live returns no count at all rather than a count of zero, and polyemesis reports the difference. Twitch publishes no encoder health on this endpoint: there is no bitrate, framerate or dropped-frame figure to show beside the viewer number.",
      moderation:
        "Delete a message, over moderator:manage:chat_messages. An account connected before this existed holds a token without that scope — the account list says so and asks you to reconnect, rather than letting the delete button fail on the message you needed gone. Twitch refuses to delete anything older than six hours, and refuses the broadcaster's own messages and other moderators'. Banning and timing out work over moderator:manage:banned_users, which is a separate scope from deletion because removing a person is a bigger ask than removing a message.",
    },
  },
  {
    presetId: "facebook",
    name: "Facebook Live",
    connect: "facebook",
    tier: "integrated",
    summary:
      "Connect a Facebook profile or Page and polyemesis creates the broadcast, splits out the RTMPS ingest and key, pushes the title and description, and reads the comment thread.",
    readFirst:
      "Meta requires App Review before anyone other than you can connect an account. Your own account works immediately as a developer or tester of your app, which is all a single-operator setup needs — but publishing on someone else's behalf needs Advanced Access to publish_video (profiles) or pages_manage_posts plus pages_read_engagement (Pages). Budget days, not minutes, and start it before you need it. Separately, and NOT a permission: Meta refuses go-live on account properties no scope can satisfy. Since 2024-06-10 the account must be at least 60 days old and the Page or professional-mode profile must have at least 100 followers. A connection with every scope granted still fails both, and the Graph error names neither — so if everything looks correct and it still will not start, check those two first.",
    caps: {
      sso: "yes",
      streamKey: "yes",
      metadata: "yes",
      chatRead: "yes",
      chatSend: "no",
      moderation: "yes",
      viewerStats: "unknown",
      broadcastLifecycle: "yes",
    },
    reasons: {
      // Facebook and YouTube both read "Works" here and mean different things:
      // YouTube is driven by the lifecycle coordinator, Facebook is commanded by
      // hand. Without this sentence an operator reads the two cells as equal and
      // expects an automatic end that nothing issues. Mirrors capabilities.go.
      broadcastLifecycle:
        'Creating the broadcast and ending it both work, but they are things YOU do — connecting the account creates the live video, and "End broadcast" is on the destination menu. Nothing ends a Facebook broadcast on its own, unlike YouTube.',
      streamKey:
        "Connecting the account is what creates the broadcast: the key polyemesis fetches belongs to one live video and a refresh starts a new one. Facebook does also offer a PERSISTENT stream key — Live Producer → Advanced settings — which is reusable every time you go live, but Meta's API exposes no way to read it, so that one is copied from Live Producer and pasted here by hand. It does not go stale. Its one limit is Facebook's: a persistent key carries one live video at a time, so two destinations cannot share it.",
      moderation:
        "Delete a comment, or HIDE one — Facebook is the only platform here that can take a message off the public thread without destroying it, because its live chat is a comment thread. Acting on a Page's comments needs the MODERATE task permission, which is separate from being able to read them, so an app that shows you the thread can still be refused when you act on it.",
      metadata:
        "Title and description. Facebook removed overlay_url in Graph API v24.0, so there is no overlay field to push.",
      chatRead:
        "Facebook's live chat is the comment thread on the live video, read over the Graph API. A destination whose key was FETCHED carries the live-video id inside the key. One whose key was PASTED does not, so polyemesis asks the connected account which broadcast it is running — which works because Facebook allows a persistent key one live video at a time. If the account has two at the same status it refuses rather than guessing, names both, and the chat pane says so; attaching to the wrong comment thread is worse than attaching to none.",
    },
  },
  {
    presetId: "kick",
    name: "Kick",
    connect: "kick",
    tier: "integrated",
    summary:
      "Sign in with Kick and polyemesis fetches the ingest URL and stream key, sets the title, category and tags, reads and replies to chat, and reads viewer stats.",
    caps: {
      sso: "yes",
      streamKey: "yes",
      metadata: "yes",
      chatRead: "yes",
      chatSend: "yes",
      moderation: "yes",
      viewerStats: "yes",
      broadcastLifecycle: "no",
    },
    reasons: {
      sso: "OAuth 2.1, which requires PKCE. Kick is the first polyemesis provider that uses it.",
      streamKey:
        "Fetched from the channels resource, over the streamkey:read scope. This was recorded here as impossible for a long time and the reasoning is worth keeping: the key rides as stream.key on the very same GET /public/v1/channels response that channel:read already fetches, but the field is omitted unless streamkey:read was granted too. There is no /streamkey endpoint, so reading the endpoint list suggests the capability does not exist. An account connected before that scope was requested holds a token without it and sees no key — reconnect it in Settings → Platforms.",
      metadata:
        "Stream title, category and up to ten custom tags, over PATCH /public/v1/channels.",
      chatRead:
        "Kick delivers chat by webhook rather than a socket, so polyemesis needs a public HTTPS URL it can be reached on. Without one the pane is silent, and it warns you rather than letting silence look like a quiet chat.",
      moderation:
        "Delete a message, over moderation:chat_message:manage. Banning and timing out work over moderation:ban. Note that Kick counts timeouts in MINUTES where YouTube and Twitch count seconds, and caps them at 7 days; polyemesis converts, so you give it one unit everywhere.",
      viewerStats:
        "Live state and viewer count from Kick's livestreams endpoints.",
    },
  },
  {
    presetId: "trovo",
    name: "Trovo",
    connect: "trovo",
    /* Integrated, not partial: the KEY is fetched over channel_details_self.
       What Trovo publishes nowhere is the ingest HOSTNAME, which is regional
       and lives only in the creator dashboard — a different field, and a
       one-time copy rather than a per-broadcast chore. */
    tier: "integrated",
    summary:
      "Sign in with Trovo and polyemesis fetches the stream key, sets the title and category at go-live, and reads the live viewer count. The server URL is the one field you copy across yourself, once.",
    readFirst:
      "Trovo does not hand out the client secret from its developer portal. Its documentation says, verbatim, “If you don't have the Client Secret, please contact: developer@trovo.live” — and the authorization-code flow cannot complete without one, because Trovo documents no PKCE. Ask for it before the day you need it. Nothing else here is gated: registration is open and every capability below is in the published reference.",
    caps: {
      sso: "yes",
      streamKey: "yes",
      metadata: "yes",
      /* Documented and unbuilt. "unknown" renders as Unverified, which invites
         the operator to try rather than refusing — the same reading the X row
         records. These are NOT absent APIs; the reasons name each endpoint. */
      chatRead: "unknown",
      chatSend: "unknown",
      moderation: "unknown",
      viewerStats: "yes",
      broadcastLifecycle: "no",
    },
    reasons: {
      sso: "OAuth 2.0 authorization code with a client_secret. Trovo documents no PKCE, so polyemesis does not send one — and the secret arrives by email from developer@trovo.live rather than from the portal.",
      streamKey:
        "Fetched from GET /openplatform/channel over the channel_details_self scope. THE SERVER URL IS NOT: Trovo issues the ingest hostname per region and publishes it nowhere in its API, so that one field is copied from the creator dashboard once and refreshing the key afterwards leaves it alone. An account connected before this integration existed has to be reconnected once.",
      metadata:
        "Title and category, over POST /openplatform/channels/update with channel_update_self. Trovo's channel update takes no description and has no tags, so those are reported as skipped rather than silently dropped.",
      chatRead:
        "Trovo delivers chat over a websocket rather than by poll or webhook. Real, documented, and not wired up here yet.",
      chatSend:
        "POST /openplatform/chat/send, over chat_send_self plus send_to_my_channel. Not wired up here yet, and the scopes are deliberately not requested until it is — asking for them early would put permissions on the consent screen that nothing uses.",
      moderation:
        "POST /openplatform/channels/command with manage_messages, which performs Trovo's own chat commands — “ban xxx”, “mod @xxx”, with no leading slash. Not wired up here yet; the scope is not requested until it is.",
      viewerStats:
        "Live state and viewer count from the same channel read the stream key comes from, so it costs no extra call and no extra scope. It reads current_viewers rather than Trovo's dedicated viewers endpoint on purpose: that endpoint's total is documented as the channel's “total login users”, which counts signed-in viewers only and would under-report an audience without saying so. Offline, polyemesis reports no count rather than zero.",
      broadcastLifecycle:
        "Trovo has no broadcast resource: nothing creates, starts or ends one, so the stream itself is the trigger and liveness can only be observed. Established by reading the whole APIs reference, not by failing to find a page.",
    },
  },
  {
    presetId: "vimeo",
    name: "Vimeo Livestream",
    connect: "vimeo",
    /*  The first row in this tier since Kick left it, and the tier's own note
     *  predicted the shape: a provider can ship SSO long before it exposes a
     *  key endpoint. Vimeo signs in on any plan and hands over no key on any
     *  plan, so "partial" is the accurate description rather than a
     *  compromise. */
    tier: "partial",
    summary:
      "Sign in with Vimeo and polyemesis reads which member the token belongs to and checks, at the moment you connect, whether your account can reach Vimeo's live API. The ingest URL and stream key are pasted from the live event: Vimeo issues them per event, and creating one is Enterprise-only.",
    readFirst:
      'Read this first: "our live API is available only to Vimeo Enterprise customers" — Vimeo\'s own words on its live API reference, read 2026-08-26. That is a commercial gate, not a permission: no scope, no reconnection and no app setting lifts it, and it applies to every live method Vimeo publishes (create an event, activate it, end it, read its ingest, its M3U8 playback and its thumbnails). Sign-in itself is open to any Vimeo plan, and polyemesis asks the live API whether YOUR account reaches it the moment you connect, so you find out then rather than mid-broadcast. Streaming to Vimeo works regardless — you paste the RTMPS URL and key from the event\'s setup panel, exactly as before. Vimeo is also deprecating one-time live events and recommends avoiding them, so create a recurring event.',
    caps: {
      sso: "yes",
      streamKey: "manual",
      /*  Every cell below renders as "Unverified" and not one of them means
       *  what the legend says. Metadata and start/end were CONFIRMED, from
       *  Vimeo's own live reference — they are unbuilt, not unchecked. None of
       *  the four support values says "documented and unbuilt"; the X row above
       *  records the same gap. Unknown is the least wrong because it is the
       *  fail-open one: "no" would be a refusal Vimeo's reference contradicts,
       *  and "yes" would be a promise no code keeps. */
      metadata: "unknown",
      chatRead: "unknown",
      chatSend: "unknown",
      moderation: "unknown",
      viewerStats: "unknown",
      broadcastLifecycle: "unknown",
    },
    reasons: {
      sso: "OAuth 2.0 authorization code against api.vimeo.com, over the public and private scopes. Vimeo can also verify your client ID and secret before you connect anything, so a typo is caught on the credentials page. PKCE is not documented for Vimeo and is therefore not sent — an authorization server that validates its query string strictly refuses an unknown parameter outright.",
      streamKey:
        "Vimeo has no permanent stream key: the ingest URL and key belong to a live event, and creating one is behind the Enterprise gate. So this is a paste, from the event's setup panel. polyemesis asks the live API which reason applies to your account and says so rather than assuming.",
      metadata:
        'Vimeo publishes "Update an event", and it is one of the methods the Enterprise gate covers. Nothing here calls it, so this is not built — but it is not unknown either, and the Unverified label overstates the doubt.',
      broadcastLifecycle:
        'Vimeo publishes "Activate an event" and "End an event", so the lifecycle polyemesis models maps cleanly onto it. Both are Enterprise-only and neither is wired up, so nothing here starts or ends a Vimeo broadcast today. Note also that Vimeo is deprecating one-time live events and recommends avoiding them; anything built here should target recurring events.',
      viewerStats:
        "Vimeo publishes a VPaaS viewer analytics EXPORT on live events, which is not the same thing as a live concurrent count, and it sits behind the same Enterprise gate. Whether a live count is readable at all is genuinely unchecked, so this cell means what the legend says.",
    },
  },
  {
    presetId: "x",
    name: "X (Twitter) Live",
    tier: "manual",
    summary:
      "Paste your ingest URL and stream key. X does publish a live-video API — broadcasts, chat and moderation — and polyemesis has not wired it up yet, so for now this is a paste-the-key destination that streams exactly as well as any other.",
    readFirst:
      "X's live-video API is real but its access tier is not published. Every capability here comes from X's own served OpenAPI spec, and no pricing page names the Broadcasts family — so whether your account can call them is a question only a live request answers. Paste the stream key from X's producer tooling; the rest is automatic once you connect.",
    caps: {
      sso: "unknown",
      streamKey: "manual",
      metadata: "unknown",
      chatRead: "unknown",
      chatSend: "unknown",
      moderation: "unknown",
      viewerStats: "unknown",
      broadcastLifecycle: "unknown",
    },
    reasons: {
      sso: "Nothing to sign into for live video. An OAuth app here would grant access to posts, which is not what a restreamer needs.",
    },
  },
  {
    presetId: "rumble",
    name: "Rumble",
    tier: "manual",
    summary:
      "Paste your ingest URL and stream key from Rumble Studio. Chat is different: Rumble's live-stream API hands over the chat with a key from your own account settings, so the pane works without any sign-in.",
    readFirst:
      "The chat key is NOT the stream key and is not pasted into polyemesis's UI. It comes from rumble.com/account/livestream-api and is supplied in the RUMBLE_CHAT_API_KEY environment variable, because there is no account to store it against — this API has no sign-in. Treat that URL as a secret: it is the whole credential, and anyone holding it can read your chat.",
    caps: {
      sso: "no",
      streamKey: "manual",
      metadata: "manual",
      chatRead: "yes",
      chatSend: "no",
      moderation: "no",
      viewerStats: "unknown",
      broadcastLifecycle: "no",
    },
    reasons: {
      chatRead:
        "Polled from rumble.com/-livestream-api/get-data, which needs no sign-in — the key from your account settings is the whole credential. Chat only exists while you are live, and the pane says it is waiting rather than going quiet. Two caveats worth knowing before you rely on it: Rumble sends no message id, so polyemesis derives one from the content, and two identical messages from one person inside the same second are indistinguishable and show as one. And Rumble publishes no rate limit, so the poll is deliberately conservative at ten seconds rather than as fast as it could be.",
      chatSend:
        "Unverified rather than impossible. Rumble's get-data endpoint returns data and no endpoint for posting or deleting a message is published — but this API is documented thinly enough that “we could not find it” is not the same as “it is not there”, so polyemesis does not refuse on the strength of it.",
      streamKey:
        "Rumble Studio issues both fields per stream; copy them across by hand.",
    },
  },
  manualUnverified(
    "dlive",
    "DLive",
    "Paste your ingest URL and stream key from DLive → Dashboard → Stream settings. Streaming works; there is no integration to connect.",
    "DLive's developer portal at dev.dlive.tv no longer resolves in DNS, so its developer support appears to be inactive. Nothing about streaming to DLive depends on that — but do not go looking for an API key, because there is currently nowhere to get one.",
  ),
  manualUnverified(
    "odysee",
    "Odysee",
    "Paste your ingest URL and stream key from Odysee. Streaming works; there is no integration to connect.",
    "Odysee's chat is the LBRY comment server, and both comments.odysee.com and comments.lbry.com answered 502 when last checked. A 502 is an outage rather than a removal, so this is unverified rather than unsupported -- but there is nothing to build against while it stays that way.",
  ),
  manualUnverified(
    "dailymotion",
    "Dailymotion",
    "Paste your ingest URL and stream key from Dailymotion. Streaming works; there is no integration to connect.",
    "api.dailymotion.com is live and openly readable. Whether it exposes live-stream chat has not been checked, so every column below is genuinely unknown rather than known to be absent.",
  ),
  manualUnverified(
    "tiktok",
    "TikTok LIVE",
    "Paste the server URL and key TikTok issues for the broadcast. Streaming works; there is no integration to connect.",
    "TikTok's developer APIs are real and answering — open.tiktokapis.com returns a structured auth error rather than a 404 — but the live surface is gated behind a partner programme, not open registration. Nothing here is reachable by pasting a token from a developer console.",
  ),
  manualUnverified(
    "linkedin",
    "LinkedIn Live",
    "Paste the ingest URL and key LinkedIn issues for the event. Streaming works; there is no integration to connect.",
    "LinkedIn Live requires approved broadcast-partner status, and its APIs sit behind the Marketing Developer Platform rather than open registration. api.linkedin.com answers, so the surface exists — but access to it is granted, not requested.",
  ),
  {
    presetId: "instagram",
    name: "Instagram Live",
    tier: "unsupported",
    summary:
      "polyemesis cannot stream to Instagram. Instagram's platform covers messaging, content publishing and comments — there is no Live broadcast API, and Live Producer's RTMP path was removed for most accounts.",
    readFirst:
      "This entry exists to save you the evening. A destination that silently never connects is worse than no destination at all: it looks like a bug in polyemesis, and there is nothing to fix. If your account still has Live Producer RTMP access, add a Generic RTMPS destination and paste the server URL and key Meta gives you — but check that you have it before you build the show around it.",
    caps: {
      sso: "no",
      // Not "manual": for most accounts there is no key to paste, so offering
      // the paste field as the answer would be its own lie.
      streamKey: "no",
      metadata: "no",
      chatRead: "no",
      chatSend: "no",
      moderation: "no",
      viewerStats: "no",
      broadcastLifecycle: "no",
    },
  },
];

/** The row for a preset id.
 *
 *  A preset with no row is the common case — thirty-odd entries, eight with
 *  anything to say beyond "paste the key" — so this returns a manual-tier row
 *  rather than nothing. Every unlisted capability then reads through
 *  `supportOf` as "unknown", which is the fail-open answer: claiming "no"
 *  about an API nobody here has read is how a capability check starts refusing
 *  things that work. */
export function capabilityFor(
  presetId: string,
  name?: string,
): PlatformCapability {
  const row = PLATFORM_CAPABILITIES.find((p) => p.presetId === presetId);
  if (row) return row;
  return {
    presetId,
    name: name || presetId,
    tier: "manual",
    summary:
      "Paste the ingest URL and stream key from this platform's dashboard. Every polyemesis feature on this side of the wire — per-destination audio routing, renditions, reconnect, meters — works exactly the same.",
    caps: { streamKey: "manual" },
  };
}

/** An absent capability is unverified, never unsupported. */
export function supportOf(row: PlatformCapability, c: Capability): Support {
  return row.caps[c] ?? "unknown";
}

export function supportInfo(s: Support) {
  return SUPPORT_LEGEND.find((l) => l.key === s) ?? SUPPORT_LEGEND[2];
}

export function tierInfo(t: CapTier) {
  return TIER_LEGEND.find((l) => l.key === t) ?? TIER_LEGEND[2];
}
