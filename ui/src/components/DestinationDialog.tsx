import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { Check, ChevronsUpDown, ExternalLink, Loader2, Search } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { api } from "@/lib/api";
import { Switch } from "@/components/ui/switch";
import type {
  Destination,
  DestTransport,
  DestResilience,
  DestKind,
  Platform,
  PlatformAccount,
  Rendition,
} from "@/lib/types";

/** The transport a preset's ingest is reached over. Finer-grained than DestKind
 *  on purpose: rtmp and rtmps are one DestKind but two different things to tell
 *  an operator, and "hls" names a transport we can give guidance about without
 *  being able to publish to it. */
type PresetTransport = "rtmp" | "rtmps" | "srt" | "hls";

type PresetGroupKey = "major" | "video" | "selfhosted" | "cloud" | "generic";

interface DestPreset {
  id: string;
  name: string;
  group: PresetGroupKey;
  transport: PresetTransport;
  /** The destination transport this preset creates. Undefined means polyemesis
   *  cannot publish over it yet; the entry exists to say so rather than to leave
   *  the operator searching for something that is not here. */
  kind?: DestKind;
  /** Only the presets with integration code behind them carry a platform.
   *  Everything else saves as "custom", which is what lets this list grow
   *  without touching destination validation. */
  platform?: Platform;
  /** Ingest template; a {placeholder} marks what the operator must replace.
   *  Empty means we do not know the hostname and will not invent one. */
  url: string;
  separateKey: boolean;
  helpUrl?: string;
  notes: string;
  /** Extra search terms, because the name an operator types is not always the
   *  name on the tin. */
  aliases?: string[];
}

/** Shown beside the catalogue, always. Ingest hostnames and platform limits
 *  move without notice, so no preset is presented as authoritative. */
const PRESET_DISCLAIMER =
  "Starting point — ingest URLs and limits change, so verify with the platform before you go live.";

const PRESET_GROUPS: { key: PresetGroupKey; label: string }[] = [
  { key: "major", label: "Major" },
  { key: "video", label: "Video platforms" },
  { key: "selfhosted", label: "Self-hosted" },
  { key: "cloud", label: "CDN & cloud" },
  { key: "generic", label: "Generic" },
];

/** Mirrors db.DestinationPresets() in internal/db/platforms.go — the same
 *  catalogue is also served from GET /api/v1/platforms/presets for scripted
 *  setups. Where a platform issues its ingest per account, per event or per
 *  region — which is most of them — url is empty and the note says where to
 *  copy it from. A blank field costs one visit to a dashboard the operator
 *  already has open; a guessed hostname costs them a broadcast, and it fails as
 *  a connection timeout that looks like their own network. */
const PRESETS: DestPreset[] = [
  {
    id: "youtube",
    name: "YouTube Live",
    group: "major",
    transport: "rtmp",
    kind: "rtmp",
    platform: "youtube",
    url: "rtmp://a.rtmp.youtube.com/live2",
    separateKey: true,
    helpUrl: "https://support.google.com/youtube/answer/2907883",
    notes:
      "Connect a Google account in Settings → Platform credentials to fetch the stream key automatically, or paste it from YouTube Studio → Go live → Stream. YouTube Studio also shows a backup ingest URL; add it as a second destination if you want redundancy.",
    aliases: ["google", "yt"],
  },
  {
    id: "youtube-rtmps",
    name: "YouTube Live (RTMPS)",
    group: "major",
    transport: "rtmps",
    kind: "rtmp",
    platform: "youtube",
    url: "rtmps://a.rtmps.youtube.com/live2",
    separateKey: true,
    helpUrl: "https://support.google.com/youtube/answer/2907883",
    notes:
      "The same ingest over TLS on 443, for networks that block port 1935. The stream key is identical to the plain-RTMP one.",
    aliases: ["google", "yt", "tls"],
  },
  {
    id: "twitch",
    name: "Twitch",
    group: "major",
    transport: "rtmp",
    kind: "rtmp",
    platform: "twitch",
    url: "rtmp://live.twitch.tv/app",
    separateKey: true,
    helpUrl: "https://help.twitch.tv/s/article/broadcast-guidelines",
    notes:
      "Connect a Twitch account in Settings → Platform credentials to fetch the stream key automatically. live.twitch.tv resolves to a nearby ingest; if it picks a poor one, copy a specific regional ingest host from Twitch's ingest list and paste it here instead.",
    aliases: ["ttv"],
  },
  {
    id: "twitch-rtmps",
    name: "Twitch (RTMPS)",
    group: "major",
    transport: "rtmps",
    kind: "rtmp",
    platform: "twitch",
    url: "rtmps://live.twitch.tv/app",
    separateKey: true,
    helpUrl: "https://help.twitch.tv/s/article/broadcast-guidelines",
    notes:
      "RTMP over TLS on 443, for networks that block 1935. Twitch's TLS support is per-ingest: if this host refuses the connection, use the plain Twitch preset or copy an RTMPS ingest from Twitch's ingest list.",
    aliases: ["ttv", "tls"],
  },
  {
    id: "kick",
    name: "Kick",
    group: "major",
    transport: "rtmps",
    kind: "rtmp",
    platform: "kick",
    url: "rtmps://fa723fc1b171.global-contribute.live-video.net",
    separateKey: true,
    helpUrl: "https://kick.com/dashboard/settings/stream",
    notes:
      "Kick is the one platform where the key stays manual: its public API exposes the channel, chat and viewer counts but no stream key anywhere. Copy both the ingest URL and the key from Kick → Settings → Stream. Connecting a Kick account in Settings → Platform credentials is still worth doing — it pushes your title and category and reports viewer counts. Kick issues the ingest host per channel, so replace the one prefilled here with yours if it differs.",
  },
  {
    id: "facebook",
    name: "Facebook Live",
    group: "major",
    transport: "rtmps",
    kind: "rtmp",
    platform: "facebook",
    url: "rtmps://live-api-s.facebook.com:443/rtmp/",
    separateKey: true,
    helpUrl: "https://www.facebook.com/live/producer",
    notes:
      "Connect a Facebook account in Settings → Platform credentials and polyemesis creates the broadcast and fills in both the ingest URL and the key. Note that Facebook issues them per broadcast, so each refresh starts a new live video rather than re-reading an existing one. Registering the Meta app is the slow part — it needs App Review before anyone but you can connect. To do it by hand instead, copy the server URL and key from Live Producer. Facebook requires RTMPS; plain RTMP is refused.",
    aliases: ["meta", "fb"],
  },
  {
    id: "instagram",
    name: "Instagram Live",
    group: "major",
    transport: "rtmps",
    kind: "rtmp",
    url: "",
    separateKey: true,
    helpUrl: "https://developers.facebook.com/docs/instagram-platform",
    notes:
      "Not supported, and this preset exists to say so rather than to be used. Instagram publishes no Live broadcast API — its platform covers messaging, content publishing and comments — and Live Producer's RTMP option was withdrawn for most accounts. If your account is one of the few that still has it, the server URL and key come from Live Producer and change every broadcast; otherwise there is nothing to paste here and no amount of configuration will change that.",
    aliases: ["meta", "ig"],
  },
  {
    id: "tiktok",
    name: "TikTok LIVE",
    group: "major",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes:
      "TikTok issues the server URL and key per broadcast, and only to accounts granted LIVE access. Create the stream in TikTok LIVE Studio or LIVE Center and copy both fields from there.",
  },

  {
    id: "x",
    name: "X (Twitter) Live",
    group: "video",
    transport: "rtmps",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes:
      "Manual key only. X's API covers posts, users, media and the post firehose — “streaming” in its documentation means streaming posts, not ingesting video — so there is no documented endpoint for polyemesis to fetch an ingest from, and none is planned. Create the source in X's own producer tooling and copy the server URL and key from there.",
    aliases: ["twitter", "periscope"],
  },
  {
    id: "linkedin",
    name: "LinkedIn Live",
    group: "video",
    transport: "rtmps",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes:
      "LinkedIn issues the ingest URL and key per event, either from LinkedIn's own event creation flow or from the approved streaming tool linked to the page. Copy both from the event.",
  },
  {
    id: "trovo",
    name: "Trovo",
    group: "video",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes:
      "Copy the server URL and stream key from the Trovo creator dashboard → Stream. Trovo's ingest hostname varies by region, so nothing is prefilled here.",
  },
  {
    id: "dlive",
    name: "DLive",
    group: "video",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes:
      "Copy the server URL and stream key from DLive → Dashboard → Stream settings. Manual only, and likely to stay that way: DLive's developer portal at dev.dlive.tv no longer resolves, so there is nothing published to integrate against.",
  },
  {
    id: "rumble",
    name: "Rumble",
    group: "video",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes:
      "Manual key only. Rumble Studio issues an ingest URL and key per stream — set the stream up there and copy both fields from its RTMP details. Rumble's API page (rumble.com/account/api) is behind a login wall with nothing published, so there is no integration to build against.",
  },
  {
    id: "odysee",
    name: "Odysee",
    group: "video",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes:
      "Odysee issues a livestream ingest URL and key from the livestream setup on your channel. Copy both from there.",
    aliases: ["lbry"],
  },
  {
    id: "vimeo",
    name: "Vimeo Livestream",
    group: "video",
    transport: "rtmps",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes:
      "Vimeo issues an RTMPS URL and key per live event. Open the event in Vimeo and copy the server URL and stream key from its setup panel.",
  },
  {
    id: "dailymotion",
    name: "Dailymotion",
    group: "video",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes:
      "Dailymotion Studio issues an ingest URL and key per live video. Copy both from the live video's settings.",
  },

  {
    id: "peertube",
    name: "PeerTube",
    group: "selfhosted",
    transport: "rtmp",
    kind: "rtmp",
    url: "rtmp://{peertube-host}:1935/live",
    separateKey: true,
    helpUrl: "https://docs.joinpeertube.org/",
    notes:
      "Replace {peertube-host} with your instance's hostname and paste the live stream key from the video's Live settings. 1935 is the default RTMP port; an instance with RTMPS enabled listens on its own port, so check that instance's live configuration.",
  },
  {
    id: "owncast",
    name: "Owncast",
    group: "selfhosted",
    transport: "rtmp",
    kind: "rtmp",
    url: "rtmp://{owncast-host}:1935/live",
    separateKey: true,
    helpUrl: "https://owncast.online/docs/broadcasting/",
    notes:
      "Replace {owncast-host} with your Owncast server. The stream key is the one configured in Owncast's admin under Server Setup, where the RTMP port can also be changed from the default 1935.",
  },
  {
    id: "wowza-engine",
    name: "Wowza Streaming Engine (self-hosted)",
    group: "selfhosted",
    transport: "rtmp",
    kind: "rtmp",
    url: "rtmp://{wowza-host}:1935/{application}",
    separateKey: true,
    notes:
      "Replace {wowza-host} and {application} with your Streaming Engine host and application name, and put the stream name in the stream key field. For the hosted product use the Wowza Video preset instead.",
  },

  {
    id: "cloudflare-stream",
    name: "Cloudflare Stream",
    group: "cloud",
    transport: "rtmps",
    kind: "rtmp",
    url: "rtmps://live.cloudflare.com:443/live/",
    separateKey: true,
    helpUrl: "https://developers.cloudflare.com/stream/stream-live/",
    notes:
      "Create a live input in Cloudflare Stream and paste its stream key. The dashboard shows the exact RTMPS URL beside the key — use that if it differs from the one prefilled here. The same input also accepts SRT and WHIP.",
    aliases: ["cf"],
  },
  {
    id: "mux",
    name: "Mux Video",
    group: "cloud",
    transport: "rtmps",
    kind: "rtmp",
    url: "rtmps://global-live.mux.com:443/app",
    separateKey: true,
    helpUrl: "https://docs.mux.com/guides/video/stream-live-to-mux",
    notes:
      "Create a live stream in Mux and paste its stream key. Mux publishes plain-RTMP and SRT endpoints for the same stream; the dashboard shows the exact URLs if you want one of those instead.",
  },
  {
    id: "aws-ivs",
    name: "AWS IVS",
    group: "cloud",
    transport: "rtmps",
    kind: "rtmp",
    url: "rtmps://{ingest-endpoint}:443/app/",
    separateKey: true,
    helpUrl: "https://docs.aws.amazon.com/ivs/",
    notes:
      "Replace {ingest-endpoint} with the channel's ingest endpoint from the IVS console and paste the channel's stream key. Each channel gets its own endpoint, so this template is not usable as-is.",
    aliases: ["amazon", "interactive video service"],
  },
  {
    id: "restream",
    name: "Restream.io relay",
    group: "cloud",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: true,
    helpUrl: "https://support.restream.io/",
    notes:
      "Restream assigns an ingest close to you, so there is no single hostname to prefill. Copy the server URL and key from Restream → Stream with RTMP. Note that relaying through Restream re-encodes your audio, which undoes per-destination track routing — send tracks straight to each platform where you can.",
  },
  {
    id: "castr",
    name: "Castr",
    group: "cloud",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes:
      "Castr issues an ingest URL and key per stream. Copy both from the stream's ingest panel in the Castr dashboard.",
  },
  {
    id: "livepush",
    name: "Livepush",
    group: "cloud",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes:
      "Copy the ingest URL and stream key from the Livepush dashboard for the channel you are pushing to.",
  },
  {
    id: "boxcast",
    name: "BoxCast",
    group: "cloud",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes:
      "BoxCast issues an ingest URL and key per broadcast source. Create the source in the BoxCast dashboard and copy both fields.",
  },
  {
    id: "wowza-video",
    name: "Wowza Video (hosted)",
    group: "cloud",
    transport: "rtmps",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes:
      "Wowza Video issues a primary ingest URL and stream name per live stream, and may also require publishing credentials. Copy them from the live stream's source connection details.",
  },
  {
    id: "akamai-msl",
    name: "Akamai Media Services Live",
    group: "cloud",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes:
      "Akamai issues per-stream entry point hostnames and a stream ID in Media Services Live, and they are account-specific. Copy the primary entry point and stream ID from the stream's configuration in Akamai Control Center.",
    aliases: ["msl"],
  },
  {
    id: "azure-media",
    name: "Azure live events",
    group: "cloud",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: false,
    notes:
      "A live event issues its own ingest URL once created and started; copy it from the event in the Azure portal — the key, where there is one, is already part of that URL. Azure Media Services was retired in June 2024, so confirm which live product your subscription actually has before relying on this.",
    aliases: ["microsoft", "ams"],
  },

  {
    id: "generic-rtmp",
    name: "Generic RTMP",
    group: "generic",
    transport: "rtmp",
    kind: "rtmp",
    url: "rtmp://{host}/{application}",
    separateKey: true,
    notes:
      "Any RTMP ingest. Replace {host} and {application}; the stream key is joined onto the URL with a slash when the stream starts, so leave it out of the URL itself.",
  },
  {
    id: "generic-rtmps",
    name: "Generic RTMPS",
    group: "generic",
    transport: "rtmps",
    kind: "rtmp",
    url: "rtmps://{host}:443/{application}",
    separateKey: true,
    notes:
      "RTMP over TLS. Use it when the network blocks port 1935 or the receiver requires TLS. Otherwise identical to the generic RTMP preset.",
  },
  {
    id: "generic-srt",
    name: "Generic SRT",
    group: "generic",
    transport: "srt",
    kind: "srt",
    url: "srt://{host}:{port}?streamid={streamid}",
    separateKey: false,
    notes:
      "SRT carries everything in the URL, including the stream id, so there is no separate key field. Add &passphrase=… and &pbkeylen=… if the receiver requires encryption.",
  },
  {
    id: "generic-hls",
    name: "Generic HLS push",
    group: "generic",
    transport: "hls",
    url: "",
    separateKey: false,
    notes:
      "polyemesis cannot push HLS to a remote HTTP endpoint. It does serve HLS itself — point players at this server's /hls path — and it can send RTMP or SRT to a packager that produces HLS for you. Choosing this preset leaves the transport alone.",
    aliases: ["http", "m3u8"],
  },
];

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
      moderation: "unknown",
      viewerStats: "unknown",
    },
    reasons: {
      chatRead:
        "Polled against the Data API's daily quota, which polyemesis paces. A long broadcast can exhaust it; the chat pane says so with the reset time rather than going quiet.",
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
      moderation: "unknown",
      viewerStats: "unknown",
    },
    reasons: { metadata: "Title and category, over the channel:manage:broadcast scope." },
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
      moderation: "unknown",
      viewerStats: "unknown",
    },
    reasons: {
      streamKey:
        "Facebook issues a fresh ingest and key per broadcast, so connecting the account is what creates the broadcast. There is no permanent key to reuse.",
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
      "Sign in with Kick for chat, moderation, metadata and viewer stats — then paste the stream key, because Kick's public API does not publish one.",
    readFirst:
      "Both halves of this destination are real at once: click Connect account for everything Kick's API does offer, and paste the ingest URL and key from Kick → Settings → Stream. Neither replaces the other, and the paste is not a workaround for a broken connection.",
    caps: {
      sso: "yes",
      streamKey: "manual",
      metadata: "yes",
      chatRead: "yes",
      chatSend: "yes",
      moderation: "yes",
      viewerStats: "yes",
    },
    reasons: {
      sso: "OAuth 2.1, which requires PKCE. Kick is the first polyemesis provider that uses it.",
      streamKey:
        "Checked against Kick's published Channels, Livestreams and Users endpoints — none of them return a stream key. This is a documented absence, not a missing feature on our side, and it does not hold back anything else.",
      metadata: "Stream title, category and up to ten custom tags, over PATCH /public/v1/channels.",
      chatRead:
        "Kick delivers chat by webhook rather than a socket, so polyemesis needs a public HTTPS URL it can be reached on. Without one the pane is silent, and it warns you rather than letting silence look like a quiet chat.",
      moderation:
        "Delete a message, over moderation:chat_message:manage. Banning and timing out are not implemented and the moderation:ban scope is deliberately not requested: nothing in polyemesis bans a viewer, and asking a restreamer's audience for that power would be overreach. Use Kick's own dashboard.",
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

/** Best-effort match of a saved destination back onto a catalogue entry, so
 *  reopening one shows what it is instead of "choose a platform". An exact URL
 *  match wins; failing that the OAuth platform is enough to name it. Returning
 *  "" is fine — a destination predates the catalogue, and nothing about it
 *  changes just because we could not label it. */
function presetIdFor(d: Destination): string {
  const platform = d.platform || "custom";
  const exact = PRESETS.find((p) => p.url !== "" && p.url === d.url);
  if (exact) return exact.id;
  if (platform !== "custom") {
    const byPlatform = PRESETS.find((p) => p.platform === platform);
    if (byPlatform) return byPlatform.id;
  }
  return "";
}

/** The value the rendition picker carries for "no rendition at all".
 *  Passthrough is the absence of a row, not a row, but a <Select> needs a
 *  non-empty string to represent it. */
const PASSTHROUGH = "passthrough";

/** One rendition in a sentence, so two tiers in the same list are told apart
 *  by what they do rather than by whatever they were named. */
function renditionSpec(r: Rendition): string {
  const scaled = r.width > 0 || r.height > 0;
  // 0 on an axis means "derive it from the source", which reads as "auto"
  // rather than as a zero the user is being asked to believe in.
  const size = scaled ? `${r.width || "auto"}×${r.height || "auto"}` : "source size";
  const fps = r.fps > 0 ? `${r.fps} fps` : "source fps";
  return `${size} · ${fps} · ${r.videoBitrate} kbps · ${r.encoder}`;
}

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Existing destination to edit, or null to create. */
  destination: Destination | null;
  onSaved: () => void;
}

export function DestinationDialog({ open, onOpenChange, destination, onSaved }: Props) {
  const editing = destination !== null;

  const [name, setName] = useState("");
  const [platform, setPlatform] = useState<Platform>("custom");
  const [presetId, setPresetId] = useState("");
  const [pickerOpen, setPickerOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [kind, setKind] = useState<DestKind>("rtmp");
  const [url, setUrl] = useState("");
  const [streamKey, setStreamKey] = useState("");
  const [bitrate, setBitrate] = useState(160);
  // Muxer and socket tuning. An empty object is "no opt-in", which is what
  // every destination that predates this carries, and what the server turns
  // into no FFmpeg arguments at all.
  const [transport, setTransport] = useState<DestTransport>({});
  // Reconnect policy. Empty is "retry forever, 1s to 30s", which is what every
  // destination did before this existed.
  const [resilience, setResilience] = useState<DestResilience>({});
  const [accountId, setAccountId] = useState<string>("none");
  const [accounts, setAccounts] = useState<PlatformAccount[]>([]);
  const [renditionId, setRenditionId] = useState<string>(PASSTHROUGH);
  const [renditions, setRenditions] = useState<Rendition[]>([]);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) return;
    api.listAccounts().then(setAccounts).catch(() => setAccounts([]));
    api
      .listRenditions()
      .then((rows) => setRenditions(rows.map((r) => r.rendition)))
      .catch(() => setRenditions([]));

    setPickerOpen(false);
    setQuery("");

    if (destination) {
      setName(destination.name);
      setPlatform(destination.platform);
      setPresetId(presetIdFor(destination));
      setKind(destination.kind);
      setUrl(destination.url);
      setStreamKey(destination.streamKey);
      setBitrate(destination.audioBitrate);
      setTransport(destination.transport ?? {});
      setResilience(destination.resilience ?? {});
      setAccountId(destination.accountId ? String(destination.accountId) : "none");
      // A destination saved before renditions existed has no rendition id at
      // all, which is exactly passthrough — the same thing it has always done.
      setRenditionId(destination.renditionId ? String(destination.renditionId) : PASSTHROUGH);
    } else {
      setTransport({});
      setResilience({});
      setName("");
      setPlatform("custom");
      setPresetId("");
      setKind("rtmp");
      setUrl("");
      setStreamKey("");
      setBitrate(160);
      setAccountId("none");
      setRenditionId(PASSTHROUGH);
    }
  }, [open, destination]);

  const selectedPreset = useMemo(
    () => PRESETS.find((p) => p.id === presetId) ?? null,
    [presetId],
  );
  // What this platform can and cannot do, resolved for every preset including
  // the ones with no row of their own — capabilityFor never returns nothing.
  const caps = useMemo(
    () => (selectedPreset ? capabilityFor(selectedPreset.id, selectedPreset.name) : null),
    [selectedPreset],
  );
  // Matched on the capability row's connect id rather than on the saved
  // platform column, because the two are not the same set: Facebook signs in
  // and fetches keys today while its destinations still save as "custom".
  const platformAccounts = useMemo(
    () => (caps?.connect ? accounts.filter((a) => String(a.platform) === caps.connect) : []),
    [accounts, caps],
  );
  const selectedRendition = useMemo(
    () => renditions.find((r) => String(r.id) === renditionId) ?? null,
    [renditions, renditionId],
  );

  // Thirty entries is past the point where scanning works, so the list is
  // searchable over everything an operator might type: the name, the id, the
  // transport, and the aliases that cover renames and parent companies.
  const matches = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return PRESETS;
    return PRESETS.filter(
      (p) =>
        p.name.toLowerCase().includes(q) ||
        p.id.includes(q) ||
        p.transport.includes(q) ||
        (p.aliases ?? []).some((a) => a.includes(q)),
    );
  }, [query]);

  const applyPreset = (p: DestPreset) => {
    setPresetId(p.id);
    setPickerOpen(false);
    setQuery("");
    // A preset whose transport we cannot publish leaves the form untouched: it
    // is in the list to explain itself, not to half-configure a destination.
    if (!p.kind) return;
    // Same for a platform polyemesis cannot stream to at all. The form is left
    // alone and the warning below does the talking; half-filling it would make
    // an Instagram destination look one field away from working, which is
    // precisely the impression that generates the support request.
    if (capabilityFor(p.id, p.name).tier === "unsupported") return;
    setKind(p.kind);
    // Only the presets with integration code behind them carry a platform;
    // every other one is an ordinary custom endpoint. That is what keeps this
    // catalogue additive — it never widens what the API will accept.
    setPlatform(p.platform ?? "custom");
    setAccountId("none");
    // An empty template must never wipe a URL the operator has already pasted.
    // Most platforms issue their ingest per account or per event, and that
    // paste is the entire configuration.
    if (p.url) setUrl(p.url);
    if (!name.trim()) setName(p.name);
  };

  // Sign-in is offered wherever the platform has it, independently of whether
  // it can also hand over a key. Kick is the case that forced the split: it
  // signs in for chat, moderation, metadata and viewer stats, and the key is
  // still typed by hand. Both affordances are shown at once, each labelled for
  // what it does, rather than one of them being hidden as if it did not exist.
  const showOAuth = caps?.connect !== undefined && supportOf(caps, "sso") === "yes";
  const manualKey = caps ? supportOf(caps, "streamKey") === "manual" : false;
  const unsupported = caps?.tier === "unsupported";

  const save = async () => {
    setBusy(true);
    try {
      const payload: Partial<Destination> = {
        name: name.trim(),
        kind,
        platform,
        url: url.trim(),
        streamKey: streamKey.trim(),
        audioBitrate: bitrate,
        accountId: accountId === "none" ? null : Number(accountId),
        // null is passthrough: no encode, no process, straight off the ingest.
        renditionId: renditionId === PASSTHROUGH ? null : Number(renditionId),
        transport,
        resilience,
      };
      if (editing) {
        await api.updateDestination(destination.id, payload);
        toast.success("Destination updated.");
      } else {
        await api.createDestination(payload);
        toast.success("Destination created. Set its audio routing next.");
      }
      onSaved();
      onOpenChange(false);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not save the destination.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] max-w-md overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{editing ? "Edit destination" : "Add destination"}</DialogTitle>
          <DialogDescription>
            This destination copies its video and re-encodes only its audio, from its own
            routing profile. Put it on a rendition if the platform will not take the source
            video — a rendition re-encodes video once for everyone that shares it, and still
            leaves the audio to each destination.
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="dest-name">Name</Label>
            <Input
              id="dest-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="YouTube — main channel"
            />
          </div>

          <div className="flex flex-col gap-1">
            <Label>Platform</Label>
            <Button
              type="button"
              variant="outline"
              className="justify-between font-normal"
              aria-expanded={pickerOpen}
              onClick={() => setPickerOpen((o) => !o)}
            >
              <span className="truncate">
                {selectedPreset ? selectedPreset.name : "Choose a platform…"}
              </span>
              <ChevronsUpDown className="opacity-50" />
            </Button>

            {pickerOpen && (
              <div className="rounded-md border">
                <div className="flex items-center gap-2 border-b px-2">
                  <Search className="size-3.5 shrink-0 opacity-50" />
                  <input
                    autoFocus
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                    placeholder={`Search ${PRESETS.length} platforms…`}
                    className="w-full bg-transparent py-2 text-xs outline-none placeholder:text-muted-foreground"
                  />
                </div>
                <div className="max-h-56 overflow-y-auto p-1">
                  {PRESET_GROUPS.map((g) => {
                    const rows = matches.filter((p) => p.group === g.key);
                    if (rows.length === 0) return null;
                    return (
                      <div key={g.key}>
                        <div className="px-2 pt-2 pb-1 text-[9px] font-medium uppercase tracking-wider text-muted-foreground">
                          {g.label}
                        </div>
                        {rows.map((p) => {
                          const rowCaps = capabilityFor(p.id, p.name);
                          return (
                          <button
                            key={p.id}
                            type="button"
                            onClick={() => applyPreset(p)}
                            className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-xs hover:bg-accent"
                          >
                            <span
                              className={
                                rowCaps.tier === "unsupported"
                                  ? "flex-1 truncate text-muted-foreground line-through"
                                  : "flex-1 truncate"
                              }
                            >
                              {p.name}
                            </span>
                            {/* The one label that has to survive a scan of
                                thirty rows. A platform we cannot stream to at
                                all must never look like one that merely needs
                                a URL pasting. */}
                            {rowCaps.tier === "unsupported" && (
                              <Badge variant="warn" className="shrink-0">
                                not supported
                              </Badge>
                            )}
                            {/* Partial signs in as much as integrated does —
                                it just cannot fetch the key — so hiding the
                                hint on it would understate what Kick offers.
                                The suffix is what stops "sign in" reading as
                                "and you are done". */}
                            {(rowCaps.tier === "integrated" || rowCaps.tier === "partial") && (
                              <span className="shrink-0 text-[9px] text-muted-foreground">
                                {rowCaps.tier === "partial" ? "sign in + paste key" : "sign in"}
                              </span>
                            )}
                            {/* Saying so here saves the operator picking a
                                platform and then wondering why the URL box is
                                still empty. */}
                            {p.kind && !p.url && rowCaps.tier !== "unsupported" && (
                              <span className="shrink-0 text-[9px] text-muted-foreground">
                                URL from dashboard
                              </span>
                            )}
                            <span className="shrink-0 font-mono text-[9px] uppercase text-muted-foreground">
                              {p.transport}
                            </span>
                            {p.id === presetId && <Check className="size-3 shrink-0" />}
                          </button>
                          );
                        })}
                      </div>
                    );
                  })}
                  {matches.length === 0 && (
                    <p className="px-2 py-3 text-[11px] text-muted-foreground">
                      Nothing matches “{query}”. Any platform works: pick a generic transport
                      below and paste the URL and key from its dashboard.
                    </p>
                  )}
                </div>
              </div>
            )}
          </div>

          {/* What this platform gives you, before an hour goes into setting it
              up. It sits directly under the picker because that is the moment
              the decision is being made — not in a help page nobody opens. */}
          {caps && (
            <div
              className={
                unsupported
                  ? "flex flex-col gap-1.5 rounded-md border border-warn/40 bg-warn-dim p-2"
                  : "flex flex-col gap-1.5 rounded-md border p-2"
              }
            >
              <div className="flex items-center gap-1.5">
                <Badge variant={unsupported ? "warn" : "outline"}>{tierInfo(caps.tier).label}</Badge>
                <span className="text-[10px] text-muted-foreground">{tierInfo(caps.tier).help}</span>
              </div>
              <p className={unsupported ? "text-[11px] text-warn" : "text-[11px] text-muted-foreground"}>
                {caps.summary}
              </p>
              {caps.readFirst && (
                <p className="text-[10px] text-muted-foreground">{caps.readFirst}</p>
              )}
              <div className="flex flex-wrap gap-1">
                {CAPABILITY_COLUMNS.map((col) => {
                  const s = supportOf(caps, col.key);
                  const info = supportInfo(s);
                  return (
                    <Badge
                      key={col.key}
                      variant={info.variant}
                      className="normal-case"
                      title={`${col.label} — ${info.label}. ${caps.reasons?.[col.key] ?? info.help}`}
                    >
                      {col.label}
                      <span className="opacity-70">{info.label.toLowerCase()}</span>
                    </Badge>
                  );
                })}
              </div>
              {unsupported && (
                <p className="text-[10px] text-warn">
                  Nothing stops you saving this — if your account is one of the exceptions, paste
                  what the platform gave you and it will stream. It is unticked here so that a
                  destination which can never connect does not look like a bug in polyemesis.
                </p>
              )}
            </div>
          )}

          <div className="flex flex-col gap-1">
            <Label>Transport</Label>
            <Select value={kind} onValueChange={(v) => setKind(v as DestKind)}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="rtmp">RTMP / RTMPS</SelectItem>
                <SelectItem value="srt">SRT</SelectItem>
                <SelectItem value="file">Local file</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {showOAuth && caps?.connect && (
            <div className="flex flex-col gap-1">
              <Label>Connected account</Label>
              {platformAccounts.length > 0 ? (
                <Select value={accountId} onValueChange={setAccountId}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {/* "Not linked" is neutral wording on purpose. On Kick the
                        key is always typed, so calling the unlinked state
                        "manual" would imply the account link had failed. */}
                    <SelectItem value="none">Not linked</SelectItem>
                    {platformAccounts.map((a) => (
                      <SelectItem key={a.id} value={String(a.id)}>
                        {a.accountName}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              ) : (
                <Button variant="outline" size="sm" asChild>
                  <a href={api.connectUrl(caps.connect)}>
                    <ExternalLink /> Connect {caps.name} account
                  </a>
                </Button>
              )}
              {/* The mixed case, spelled out. Kick's sign-in is worth having on
                  its own merits — chat, moderation, metadata, viewer count —
                  and an operator who reads "connect an account" as "this is how
                  I get my key" will conclude the feature is broken when the key
                  field stays empty. */}
              <span className="text-[10px] text-muted-foreground">
                {manualKey
                  ? `Signing in gets you ${[
                      supportOf(caps, "chatRead") === "yes" && "chat",
                      supportOf(caps, "moderation") === "yes" && "moderation",
                      supportOf(caps, "metadata") === "yes" && "title and category push",
                      supportOf(caps, "viewerStats") === "yes" && "viewer counts",
                    ]
                      .filter(Boolean)
                      .join(", ")}. It does not get you the stream key — ${caps.name} does not publish one, so you type that in below. Both at once is the expected setup here, not a fallback.`
                  : "Linking an account lets polyemesis fetch the stream key for you."}{" "}
                Requires developer credentials in Settings → Platform credentials.
              </span>
            </div>
          )}

          <div className="flex flex-col gap-1">
            <Label htmlFor="dest-url">
              {kind === "file" ? "Filename" : kind === "srt" ? "SRT URL" : "RTMP URL"}
            </Label>
            <Input
              id="dest-url"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              className="font-mono"
              placeholder={
                kind === "file"
                  ? "clean-mix.mkv"
                  : kind === "srt"
                    ? "srt://host:9000?streamid=..."
                    : "rtmp://host/app"
              }
            />
            {kind === "file" && (
              <span className="text-[10px] text-muted-foreground">
                Written into the recordings directory. A relative name only — no paths.
              </span>
            )}
            {/* A preset that could not prefill a URL is not a broken preset; it
                is one whose platform issues the hostname per account. Say that
                where the empty box is, not in a tooltip somewhere else. */}
            {selectedPreset?.kind && !selectedPreset.url && kind !== "file" && (
              <span className="text-[10px] text-muted-foreground">
                {selectedPreset.name} issues its ingest URL per account or per broadcast, so
                there is nothing to prefill — paste the one from its dashboard.
              </span>
            )}
          </div>

          {kind === "rtmp" && (
            <div className="flex flex-col gap-1">
              <Label htmlFor="dest-key">Stream key</Label>
              <Input
                id="dest-key"
                type="password"
                value={streamKey}
                onChange={(e) => setStreamKey(e.target.value)}
                className="font-mono"
                placeholder="xxxx-xxxx-xxxx-xxxx"
              />
              {selectedPreset && !selectedPreset.separateKey && (
                <span className="text-[10px] text-muted-foreground">
                  {selectedPreset.name} puts everything in the URL — leave this empty unless
                  its dashboard shows a separate key.
                </span>
              )}
              {/* Said at the field, where the operator is looking for a Fetch
                  button that is never going to appear. The reason travels with
                  it so this does not read as an apology. */}
              {manualKey && caps && (
                <span className="text-[10px] text-muted-foreground">
                  {showOAuth
                    ? `Type this one in even with an account connected. ${caps.reasons?.streamKey ?? ""}`
                    : caps.reasons?.streamKey ??
                      `${caps.name} has no API to fetch a key from — copy it from the platform's dashboard.`}
                </span>
              )}
            </div>
          )}

          {/* Video and audio sit together on purpose: what a platform receives
              is one answer with two halves, and the pairing is the feature. */}
          <div className="flex flex-col gap-1">
            <Label>Video</Label>
            <Select value={renditionId} onValueChange={setRenditionId}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={PASSTHROUGH}>Passthrough — source, copied</SelectItem>
                {renditions.map((r) => (
                  <SelectItem key={r.id} value={String(r.id)}>
                    {r.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <span className="text-[10px] text-muted-foreground">
              {selectedRendition ? (
                <>
                  <span className="font-mono">{renditionSpec(selectedRendition)}</span>
                  {/* Shared, not per-destination: this is why picking a
                      rendition for a third platform costs nothing extra. */}
                  {" — one encode, shared by every destination on this rendition."}
                </>
              ) : (
                <>
                  <span className="font-mono">-c:v copy</span> — the source video, byte for byte,
                  at zero CPU cost. Pick a rendition when a platform will not accept the source.
                </>
              )}
            </span>
            {selectedRendition?.note && (
              <span className="text-[10px] text-muted-foreground">{selectedRendition.note}</span>
            )}
          </div>

          <div className="flex flex-col gap-1">
            <Label htmlFor="dest-bitrate">Audio bitrate</Label>
            <div className="flex items-center gap-2">
              <Input
                id="dest-bitrate"
                type="number"
                min={32}
                max={512}
                value={bitrate}
                onChange={(e) => setBitrate(Number(e.target.value))}
                className="w-24"
              />
              <span className="text-[11px] text-muted-foreground">kbps, AAC stereo</span>
            </div>
            <span className="text-[10px] text-muted-foreground">
              Mixed here from this destination's own routing profile, whichever rendition it is
              on — a rendition never touches audio.
            </span>
          </div>

          <details className="rounded-md border border-border p-2">
            <summary className="cursor-pointer text-xs font-medium">
              Transport &amp; muxer
              {(transport.noDurationFilesize ||
                (transport.muxQueuePackets ?? 0) > 0 ||
                (transport.rwTimeoutSeconds ?? 0) > 0) && (
                <span className="ml-2 text-[10px] text-muted-foreground">(customised)</span>
              )}
            </summary>
            <div className="mt-2 flex flex-col gap-3">
              <span className="text-[10px] text-muted-foreground">
                Leave all of these alone unless a platform is misbehaving. Every one is off by
                default, and off means polyemesis sends exactly the FFmpeg command it always has.
              </span>

              <div className="flex flex-col gap-1">
                <Label htmlFor="dest-rwt">Socket timeout (seconds)</Label>
                <Input
                  id="dest-rwt"
                  type="number"
                  min={0}
                  max={3600}
                  value={transport.rwTimeoutSeconds ?? 0}
                  onChange={(e) =>
                    setTransport({ ...transport, rwTimeoutSeconds: Number(e.target.value) })
                  }
                  className="w-24"
                />
                <span className="text-[10px] text-muted-foreground">
                  0 disables it. Worth setting on a platform behind a load balancer: a far end that
                  disappears without closing the connection otherwise blocks the muxer forever.
                  FFmpeg keeps running, polyemesis sees a healthy process, and the stream is off air
                  with nothing saying so.
                </span>
              </div>

              {kind === "rtmp" && (
                <div className="flex items-center justify-between gap-2">
                  <Label htmlFor="dest-flv" className="font-normal">
                    Drop FLV duration &amp; filesize metadata
                  </Label>
                  <Switch
                    id="dest-flv"
                    checked={transport.noDurationFilesize ?? false}
                    onCheckedChange={(v) => setTransport({ ...transport, noDurationFilesize: v })}
                  />
                </div>
              )}
              {kind === "rtmp" && (
                <span className="text-[10px] text-muted-foreground">
                  Both are necessarily zero on a live stream, and some ingests read a zero duration
                  as a broken file rather than as a live one. RTMP only.
                </span>
              )}

              <div className="grid grid-cols-2 gap-2">
                <div className="flex flex-col gap-1">
                  <Label htmlFor="dest-mqp">Muxing queue (packets)</Label>
                  <Input
                    id="dest-mqp"
                    type="number"
                    min={0}
                    value={transport.muxQueuePackets ?? 0}
                    onChange={(e) =>
                      setTransport({ ...transport, muxQueuePackets: Number(e.target.value) })
                    }
                  />
                </div>
                <div className="flex flex-col gap-1">
                  <Label htmlFor="dest-mqb">…above (bytes)</Label>
                  <Input
                    id="dest-mqb"
                    type="number"
                    min={0}
                    value={transport.muxQueueBytes ?? 0}
                    onChange={(e) =>
                      setTransport({ ...transport, muxQueueBytes: Number(e.target.value) })
                    }
                  />
                </div>
              </div>
              <span className="text-[10px] text-muted-foreground">
                These are a <strong>pair</strong>: FFmpeg applies the packet cap only once the queue
                has grown past the byte threshold, so a threshold on its own does nothing and will
                be refused. Raise them if a destination reports interleave errors &mdash; the audio
                path here has variable latency, because loudness normalisation reads ahead.
              </span>

              <div className="border-t border-border pt-3">
                <p className="text-xs font-medium">Reconnecting</p>
              </div>

              <div className="grid grid-cols-2 gap-2">
                <div className="flex flex-col gap-1">
                  <Label htmlFor="dest-minbo">Retry after (seconds)</Label>
                  <Input
                    id="dest-minbo"
                    type="number"
                    min={0}
                    max={300}
                    value={resilience.minBackoffSeconds ?? 0}
                    onChange={(e) =>
                      setResilience({ ...resilience, minBackoffSeconds: Number(e.target.value) })
                    }
                  />
                </div>
                <div className="flex flex-col gap-1">
                  <Label htmlFor="dest-maxbo">…backing off to</Label>
                  <Input
                    id="dest-maxbo"
                    type="number"
                    min={0}
                    max={300}
                    value={resilience.maxBackoffSeconds ?? 0}
                    onChange={(e) =>
                      setResilience({ ...resilience, maxBackoffSeconds: Number(e.target.value) })
                    }
                  />
                </div>
              </div>
              <span className="text-[10px] text-muted-foreground">
                0 on both takes the default, 1s doubling to 30s. A destination that stays up past a
                minute is treated as healthy and starts again from the shorter delay.
              </span>

              <div className="flex flex-col gap-1">
                <Label htmlFor="dest-giveup">Give up after (retries)</Label>
                <Input
                  id="dest-giveup"
                  type="number"
                  min={0}
                  max={1000}
                  value={resilience.giveUpAfter ?? 0}
                  onChange={(e) =>
                    setResilience({ ...resilience, giveUpAfter: Number(e.target.value) })
                  }
                  className="w-24"
                />
                <span className="text-[10px] text-muted-foreground">
                  0 retries forever, which is the default and is right for a platform that is
                  merely slow to come back. Set it when you would rather be TOLD: a destination
                  retrying forever looks exactly like one that works &mdash; the card says
                  "reconnecting" and nothing ever says this endpoint is not coming back. Giving up
                  marks it failed, which your alert rules already treat as an incident.
                  Consecutive failures only; a clean run resets the count.
                </span>
              </div>
            </div>
          </details>

          {selectedPreset && (
            <div className="flex flex-col gap-1 rounded-md border border-dashed p-2">
              <p className="text-[10px] text-muted-foreground">{selectedPreset.notes}</p>
              <p className="text-[10px] text-muted-foreground">{PRESET_DISCLAIMER}</p>
              {selectedPreset.helpUrl && (
                <a
                  href={selectedPreset.helpUrl}
                  target="_blank"
                  rel="noreferrer noopener"
                  className="inline-flex items-center gap-1 text-[10px] underline underline-offset-2"
                >
                  <ExternalLink className="size-3" /> {selectedPreset.name} setup docs
                </a>
              )}
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button onClick={save} disabled={busy || !name.trim()}>
            {busy && <Loader2 className="animate-spin" />}
            {editing ? "Save" : "Create"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
