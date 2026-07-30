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
  | "viewerStats";

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

export const CAPABILITY_COLUMNS: { key: Capability; label: string; help: string }[] = [
  { key: "sso", label: "Sign in", help: "Connect the account with OAuth instead of pasting secrets." },
  { key: "streamKey", label: "Stream key", help: "polyemesis fetches the ingest URL and key for you." },
  { key: "metadata", label: "Metadata", help: "Set the title, description or category when you go live." },
  { key: "chatRead", label: "Chat read", help: "Messages appear in the unified chat pane." },
  { key: "chatSend", label: "Chat send", help: "You can reply from the chat pane." },
  { key: "moderation", label: "Moderation", help: "Delete a message or time a viewer out." },
  { key: "viewerStats", label: "Viewers", help: "Live viewer count read back from the platform." },
];

export const SUPPORT_LEGEND: {
  key: Support;
  label: string;
  help: string;
  variant: "live" | "default" | "outline" | "warn";
}[] = [
  { key: "yes", label: "Works", help: "polyemesis does this for you today.", variant: "live" },
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
      viewerStats: "unknown",
    },
    reasons: {
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
      viewerStats: "unknown",
    },
    reasons: {
      metadata: "Title and category, over the channel:manage:broadcast scope.",
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
      "Meta requires App Review before anyone other than you can connect an account. Your own account works immediately as a developer or tester of your app, which is all a single-operator setup needs — but publishing on someone else's behalf needs Advanced Access to publish_video (profiles) or pages_manage_posts plus pages_read_engagement (Pages). Budget days, not minutes, and start it before you need it.",
    caps: {
      sso: "yes",
      streamKey: "yes",
      metadata: "yes",
      chatRead: "yes",
      chatSend: "unknown",
      moderation: "yes",
      viewerStats: "unknown",
    },
    reasons: {
      streamKey:
        "Facebook issues a fresh ingest and key per broadcast, so connecting the account is what creates the broadcast. There is no permanent key to reuse.",
      moderation:
        "Delete a comment, or HIDE one — Facebook is the only platform here that can take a message off the public thread without destroying it, because its live chat is a comment thread. Acting on a Page's comments needs the MODERATE task permission, which is separate from being able to read them, so an app that shows you the thread can still be refused when you act on it.",
      metadata:
        "Title and description. Facebook removed overlay_url in Graph API v24.0, so there is no overlay field to push.",
      chatRead:
        "Facebook's live chat is the comment thread on the live video, read over the Graph API. A destination whose key was pasted by hand has no live-video id to attach to, and the chat pane says so.",
    },
  },
  {
    presetId: "kick",
    name: "Kick",
    connect: "kick",
    tier: "partial",
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
    },
    reasons: {
      sso: "OAuth 2.1, which requires PKCE. Kick is the first polyemesis provider that uses it.",
      streamKey:
        "Fetched from the channels resource, over the streamkey:read scope. This was recorded here as impossible for a long time and the reasoning is worth keeping: the key rides as stream.key on the very same GET /public/v1/channels response that channel:read already fetches, but the field is omitted unless streamkey:read was granted too. There is no /streamkey endpoint, so reading the endpoint list suggests the capability does not exist. An account connected before that scope was requested holds a token without it and sees no key — reconnect it in Settings → Platforms.",
      metadata: "Stream title, category and up to ten custom tags, over PATCH /public/v1/channels.",
      chatRead:
        "Kick delivers chat by webhook rather than a socket, so polyemesis needs a public HTTPS URL it can be reached on. Without one the pane is silent, and it warns you rather than letting silence look like a quiet chat.",
      moderation:
        "Delete a message, over moderation:chat_message:manage. Banning and timing out work over moderation:ban. Note that Kick counts timeouts in MINUTES where YouTube and Twitch count seconds, and caps them at 7 days; polyemesis converts, so you give it one unit everywhere.",
      viewerStats: "Live state and viewer count from Kick's livestreams endpoints.",
    },
  },
  {
    presetId: "x",
    name: "X (Twitter) Live",
    tier: "manual",
    summary:
      "Paste your ingest URL and stream key. There is no API to connect: X's developer platform covers posts, users, media and the post firehose, not live-video ingest.",
    readFirst:
      "“Streaming” in X's API documentation means streaming posts, not ingesting video. No documented third-party live-video ingest endpoint exists, and access to what is documented is credit-based and paid. Set the source up in X's own producer tooling and copy both fields across.",
    caps: {
      sso: "no",
      streamKey: "manual",
      metadata: "no",
      chatRead: "no",
      chatSend: "no",
      moderation: "no",
      viewerStats: "no",
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
      "Paste your ingest URL and stream key from Rumble Studio. Rumble has an API page, but it sits behind a login and nothing about it is published.",
    readFirst:
      "rumble.com/account/api requires an account to view and documents nothing publicly, so polyemesis makes no claim about what it can or cannot do. If you have access and it turns out to offer more, that is a gap in our knowledge rather than a limit of the platform.",
    caps: {
      sso: "unknown",
      streamKey: "manual",
      metadata: "unknown",
      chatRead: "unknown",
      chatSend: "unknown",
      moderation: "unknown",
      viewerStats: "unknown",
    },
  },
  {
    presetId: "dlive",
    name: "DLive",
    tier: "manual",
    summary:
      "Paste your ingest URL and stream key from DLive → Dashboard → Stream settings. Streaming works; there is no integration to connect.",
    readFirst:
      "DLive's developer portal at dev.dlive.tv no longer resolves in DNS, so its developer support appears to be inactive. Nothing about streaming to DLive depends on that — but do not go looking for an API key, because there is currently nowhere to get one.",
    caps: {
      sso: "unknown",
      streamKey: "manual",
      metadata: "unknown",
      chatRead: "unknown",
      chatSend: "unknown",
      moderation: "unknown",
      viewerStats: "unknown",
    },
  },
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
export function capabilityFor(presetId: string, name?: string): PlatformCapability {
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
