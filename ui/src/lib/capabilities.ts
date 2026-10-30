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

import type { TranslationKey } from "@/lib/i18n";

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
  summary: TranslationKey;
  /** The thing that costs a day if it is met halfway through instead of at
   *  the start. Facebook's App Review is why this field exists. */
  readFirst?: TranslationKey;
  caps: Partial<Record<Capability, Support>>;
  /** Only the cells where the value alone would raise a question. A wall of
   *  tooltips is not honesty. */
  reasons?: Partial<Record<Capability, TranslationKey>>;
}

export const CAPABILITY_COLUMNS: {
  key: Capability;
  label: TranslationKey;
  help: TranslationKey;
}[] = [
  {
    key: "sso",
    label: "auth.signIn",
    help: "caps.colSsoHelp",
  },
  {
    key: "streamKey",
    label: "dest.streamKeyLabel",
    help: "caps.colStreamKeyHelp",
  },
  {
    key: "metadata",
    label: "caps.colMetadata",
    help: "caps.colMetadataHelp",
  },
  {
    key: "chatRead",
    label: "caps.colChatRead",
    help: "caps.colChatReadHelp",
  },
  {
    key: "chatSend",
    label: "caps.colChatSend",
    help: "caps.colChatSendHelp",
  },
  {
    key: "moderation",
    label: "caps.colModeration",
    help: "caps.colModerationHelp",
  },
  {
    key: "viewerStats",
    label: "caps.colViewerStats",
    help: "caps.colViewerStatsHelp",
  },
  {
    key: "broadcastLifecycle",
    label: "caps.colBroadcastLifecycle",
    help: "caps.colBroadcastLifecycleHelp",
  },
];

export const SUPPORT_LEGEND: {
  key: Support;
  label: TranslationKey;
  help: TranslationKey;
  variant: "live" | "default" | "outline" | "warn";
}[] = [
  {
    key: "yes",
    label: "caps.supportYes",
    help: "caps.supportYesHelp",
    variant: "live",
  },
  {
    key: "manual",
    label: "caps.supportManual",
    help: "caps.supportManualHelp",
    variant: "default",
  },
  {
    key: "unknown",
    label: "caps.supportUnknown",
    help: "caps.supportUnknownHelp",
    variant: "outline",
  },
  {
    key: "no",
    label: "caps.supportNo",
    help: "caps.supportNoHelp",
    variant: "warn",
  },
];

export const TIER_LEGEND: { key: CapTier; label: TranslationKey; help: TranslationKey }[] = [
  {
    key: "integrated",
    label: "caps.tierIntegrated",
    help: "caps.tierIntegratedHelp",
  },
  {
    key: "partial",
    label: "caps.tierPartial",
    help: "caps.tierPartialHelp",
  },
  {
    key: "manual",
    label: "caps.tierManual",
    help: "caps.tierManualHelp",
  },
  {
    key: "unsupported",
    label: "caps.tierUnsupported",
    help: "caps.tierUnsupportedHelp",
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
  summary: TranslationKey,
  readFirst: TranslationKey,
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
    summary: "caps.youtube.summary",
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
      broadcastLifecycle: "caps.youtube.broadcastLifecycle",
      viewerStats: "caps.youtube.viewerStats",
      chatRead: "caps.youtube.chatRead",
      moderation: "caps.youtube.moderation",
    },
  },
  {
    presetId: "twitch",
    name: "Twitch",
    connect: "twitch",
    tier: "integrated",
    summary: "caps.twitch.summary",
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
      metadata: "caps.twitch.metadata",
      viewerStats: "caps.twitch.viewerStats",
      moderation: "caps.twitch.moderation",
    },
  },
  {
    presetId: "facebook",
    name: "Facebook Live",
    connect: "facebook",
    tier: "integrated",
    summary: "caps.facebook.summary",
    readFirst: "caps.facebook.readFirst",
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
      broadcastLifecycle: "caps.facebook.broadcastLifecycle",
      streamKey: "caps.facebook.streamKey",
      moderation: "caps.facebook.moderation",
      metadata: "caps.facebook.metadata",
      chatRead: "caps.facebook.chatRead",
    },
  },
  {
    presetId: "kick",
    name: "Kick",
    connect: "kick",
    tier: "integrated",
    summary: "caps.kick.summary",
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
      sso: "caps.kick.sso",
      streamKey: "caps.kick.streamKey",
      metadata: "caps.kick.metadata",
      chatRead: "caps.kick.chatRead",
      moderation: "caps.kick.moderation",
      viewerStats: "caps.kick.viewerStats",
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
    summary: "caps.trovo.summary",
    readFirst: "caps.trovo.readFirst",
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
      sso: "caps.trovo.sso",
      streamKey: "caps.trovo.streamKey",
      metadata: "caps.trovo.metadata",
      chatRead: "caps.trovo.chatRead",
      chatSend: "caps.trovo.chatSend",
      moderation: "caps.trovo.moderation",
      viewerStats: "caps.trovo.viewerStats",
      broadcastLifecycle: "caps.trovo.broadcastLifecycle",
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
    summary: "caps.vimeo.summary",
    readFirst: "caps.vimeo.readFirst",
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
      sso: "caps.vimeo.sso",
      streamKey: "caps.vimeo.streamKey",
      metadata: "caps.vimeo.metadata",
      broadcastLifecycle: "caps.vimeo.broadcastLifecycle",
      viewerStats: "caps.vimeo.viewerStats",
    },
  },
  {
    presetId: "x",
    name: "X (Twitter) Live",
    tier: "manual",
    summary: "caps.x.summary",
    readFirst: "caps.x.readFirst",
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
      sso: "caps.x.sso",
    },
  },
  {
    presetId: "rumble",
    name: "Rumble",
    tier: "manual",
    summary: "caps.rumble.summary",
    readFirst: "caps.rumble.readFirst",
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
      chatRead: "caps.rumble.chatRead",
      chatSend: "caps.rumble.chatSend",
      streamKey: "caps.rumble.streamKey",
    },
  },
  manualUnverified(
    "dlive",
    "DLive",
    "caps.dlive.summary",
    "caps.dlive.readFirst",
  ),
  manualUnverified(
    "odysee",
    "Odysee",
    "caps.odysee.summary",
    "caps.odysee.readFirst",
  ),
  manualUnverified(
    "dailymotion",
    "Dailymotion",
    "caps.dailymotion.summary",
    "caps.dailymotion.readFirst",
  ),
  manualUnverified(
    "tiktok",
    "TikTok LIVE",
    "caps.tiktok.summary",
    "caps.tiktok.readFirst",
  ),
  manualUnverified(
    "linkedin",
    "LinkedIn Live",
    "caps.linkedin.summary",
    "caps.linkedin.readFirst",
  ),
  {
    presetId: "instagram",
    name: "Instagram Live",
    tier: "unsupported",
    summary: "caps.instagram.summary",
    readFirst: "caps.instagram.readFirst",
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
    summary: "caps.fallbackSummary",
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
