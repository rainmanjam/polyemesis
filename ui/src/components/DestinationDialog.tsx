import { useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { Check, ChevronsUpDown, ExternalLink, Loader2, Plus, Search, Trash2 } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Badge } from "@/components/ui/badge";
import { StatusDot } from "@/components/signature/StatusDot";
import { cn } from "@/lib/utils";
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
import { useT } from "@/lib/i18n";
import { computeLeaving, joinConsequence, leaveConsequence } from "@/lib/rendition-consequence";
import { Switch } from "@/components/ui/switch";
// The capability matrix this dialog renders inline. Data, not a component, and
// shared with the settings page — see lib/capabilities.ts.
import {
  CAPABILITY_COLUMNS,
  capabilityFor,
  supportInfo,
  supportOf,
  tierInfo,
} from "@/lib/capabilities";
import { TWITCH_LABELS } from "@/lib/types";
import type {
  Destination,
  DestTransport,
  DestResilience,
  AudioEncoding,
  Compliance,
  FacebookSettings,
  PrivacyStatus,
  DestKind,
  Platform,
  PlatformAccount,
  Rendition,
  PlatformPresetInfo,
  RenditionView,
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
  // Only the Facebook create-time block reads this so far. The rest of the
  // dialog is still English literals -- a mechanical follow-up, key by key,
  // rather than something to do halfway in a commit about drift guards.
  const t = useT();

  const [name, setName] = useState("");
  const [platform, setPlatform] = useState<Platform>("custom");
  const [presetId, setPresetId] = useState("");
  const [pickerOpen, setPickerOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [kind, setKind] = useState<DestKind>("rtmp");
  const [url, setUrl] = useState("");
  const [streamKey, setStreamKey] = useState("");
  // The key as it was when this dialog opened, so save can tell "the operator
  // did not touch this field" from "the field happens to be empty". Compared
  // against the loaded value rather than against "", because a destination
  // whose key has not been fetched yet legitimately holds "" and must still be
  // able to send one. A ref rather than state: nothing renders from it, and a
  // re-render must not reset it.
  const loadedStreamKey = useRef("");
  const [bitrate, setBitrate] = useState(160);
  // Muxer and socket tuning. An empty object is "no opt-in", which is what
  // every destination that predates this carries, and what the server turns
  // into no FFmpeg arguments at all.
  const [transport, setTransport] = useState<DestTransport>({});
  // Reconnect policy. Empty is "retry forever, 1s to 30s", which is what every
  // destination did before this existed.
  const [resilience, setResilience] = useState<DestResilience>({});
  // Output audio encoding. Empty is AAC stereo.
  const [audio, setAudio] = useState<AudioEncoding>({});
  // Copying audio is offered only where the receiver is not a platform ingest.
  // An RTMP destination is refused server-side, so the toggle is not shown for
  // it at all rather than shown and rejected on save. Derived rather than
  // stored so that changing the transport cannot leave a hidden flag set.
  const canCopyAudio = kind === "srt" || kind === "file";
  const copyOn = canCopyAudio && (audio.copy ?? false);
  // Obligation metadata. Empty means "touch nothing", which is what every
  // destination that has never set any carries.
  const [compliance, setCompliance] = useState<Compliance>({});
  // Facebook create-time settings: crossposting and the donate button. Separate
  // from compliance -- neither is an obligation, both only apply at the moment
  // the broadcast is created. Empty means "send neither".
  const [facebook, setFacebook] = useState<FacebookSettings>({});
  // "Publish a redundant feed for this destination." Top-level rather than part
  // of `facebook` because neither the endpoint it gates nor the engine that
  // reads it is platform-specific — see db.Destination.BackupIngestWanted. The
  // control is still rendered inside the Facebook box because Facebook is the
  // only platform that hands out a backup endpoint today.
  const [backupIngestWanted, setBackupIngestWanted] = useState(false);
  const [accountId, setAccountId] = useState<string>("none");
  const [accounts, setAccounts] = useState<PlatformAccount[]>([]);
  const [renditionId, setRenditionId] = useState<string>(PASSTHROUGH);
  // RenditionView, not Rendition. The view carries `destinations` and
  // `enabledDestinations`, and this used to strip them off one line after they
  // arrived — throwing away the only data that can tell an operator whether
  // picking an encode starts a new one or joins a running one. That fact is
  // the entire argument for renditions existing, and the UI could not state it.
  const [renditions, setRenditions] = useState<RenditionView[]>([]);
  const [guidance, setGuidance] = useState<PlatformPresetInfo[]>([]);
  const [busy, setBusy] = useState(false);
  // The variant form: an operator wanting "this tier but 4500 kbps for the
  // constrained uplink". Deliberately NOT a per-destination override of the
  // shared encode — there is no such thing, and there must not be: editing the
  // shared tier would silently change the picture every other destination on it
  // receives. A variant is a SECOND encode, and the UI says so before you make
  // one.
  const [variantOpen, setVariantOpen] = useState(false);
  const [variantBitrate, setVariantBitrate] = useState("");
  const [variantWidth, setVariantWidth] = useState("");
  const [variantHeight, setVariantHeight] = useState("");
  const [variantFps, setVariantFps] = useState("");
  const [variantErr, setVariantErr] = useState("");

  useEffect(() => {
    if (!open) return;
    api.listAccounts().then(setAccounts).catch(() => setAccounts([]));
    api
      .listRenditions()
      .then(setRenditions)
      .catch(() => setRenditions([]));
    // Fetched, not mirrored. The UI keeps its own preset list so the picker
    // renders before any request resolves, but the researched numbers carry a
    // source and a date and a second copy of them here would drift silently —
    // which is exactly what happened when they were added to the Go catalogue
    // and nothing surfaced them.
    api
      .platformPresets()
      .then((r) => setGuidance(r.presets))
      .catch(() => setGuidance([]));

    setPickerOpen(false);
    setQuery("");

    if (destination) {
      setName(destination.name);
      setPlatform(destination.platform);
      setPresetId(presetIdFor(destination));
      setKind(destination.kind);
      setUrl(destination.url);
      setStreamKey(destination.streamKey);
      loadedStreamKey.current = destination.streamKey;
      setBitrate(destination.audioBitrate);
      setTransport(destination.transport ?? {});
      setResilience(destination.resilience ?? {});
      setAudio(destination.audio ?? {});
      setCompliance(destination.compliance ?? {});
      setFacebook(destination.facebook ?? {});
      setBackupIngestWanted(destination.backupIngestWanted ?? false);
      setAccountId(destination.accountId ? String(destination.accountId) : "none");
      // A destination saved before renditions existed has no rendition id at
      // all, which is exactly passthrough — the same thing it has always done.
      setRenditionId(destination.renditionId ? String(destination.renditionId) : PASSTHROUGH);
    } else {
      setTransport({});
      setResilience({});
      setAudio({});
      setCompliance({});
      setFacebook({});
      setBackupIngestWanted(false);
      setName("");
      setPlatform("custom");
      setPresetId("");
      setKind("rtmp");
      setUrl("");
      setStreamKey("");
      loadedStreamKey.current = "";
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
  // Whether the connected Facebook account publishes to a PAGE rather than a
  // profile, which decides whether an audience setting means anything at all.
  //
  // A Page broadcast is public: Facebook has no personal audience for a Page,
  // and IngestFor suppresses privacy for one regardless of what is stored. So
  // the control is hidden rather than shown-and-ignored -- offering a setting
  // that silently does nothing is the defect roadmap item 0 exists for.
  //
  // Only a ref that SAYS page: counts. A legacy ref carrying no prefix resolves
  // as "auto" server-side, trying Pages first and falling back to the profile,
  // so which it is cannot be known here. Those keep the control and its hint,
  // because hiding it on a guess would take away a setting that does work.
  const facebookTargetsAPage = useMemo(() => {
    const acct = accounts.find((a) => String(a.id) === accountId);
    return acct?.accountRef?.startsWith("page:") ?? false;
  }, [accounts, accountId]);

  /** What this destination's platform says it wants. Null when the operator has
   *  not picked a preset, or when the platform publishes nothing usable — which
   *  is a real answer for TikTok and PeerTube and is shown as silence rather
   *  than as an invented number. */
  const platformVideo = useMemo(
    () => guidance.find((g) => g.id === presetId)?.video ?? null,
    [guidance, presetId],
  );

  const selectedView = useMemo(
    () => renditions.find((v) => String(v.rendition.id) === renditionId) ?? null,
    [renditions, renditionId],
  );
  const selectedRendition = selectedView?.rendition ?? null;

  /** What the encode this destination is LEAVING will do once it has gone.
   *
   *  Nothing in the field tells an operator this — MediaLive's documented
   *  remediation is "make a note of the video encode, in case you need to refer
   *  to it again". It is the same arithmetic as joining, run backwards, so it
   *  costs nothing to say. */
  useEffect(() => {
    if (!variantOpen || !selectedRendition) return;
    setVariantBitrate(String(selectedRendition.videoBitrate));
    setVariantWidth(selectedRendition.width ? String(selectedRendition.width) : "");
    setVariantHeight(selectedRendition.height ? String(selectedRendition.height) : "");
    setVariantFps(selectedRendition.fps ? String(selectedRendition.fps) : "");
    setVariantErr("");
  }, [variantOpen, selectedRendition]);

  const leaving = useMemo(
    () =>
      computeLeaving(destination?.renditionId, renditionId, destination?.enabled === true, (id) => {
        const was = renditions.find((v) => v.rendition.id === id);
        return was
          ? {
              name: was.rendition.name,
              destinations: was.destinations,
              enabledDestinations: was.enabledDestinations,
            }
          : null;
      }),
    [destination, renditionId, renditions],
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
        audioBitrate: bitrate,
        accountId: accountId === "none" ? null : Number(accountId),
        // null is passthrough: no encode, no process, straight off the ingest.
        renditionId: renditionId === PASSTHROUGH ? null : Number(renditionId),
        transport,
        resilience,
        // copyOn rather than audio.copy: switching the transport to RTMP has to
        // drop the flag here, or the operator gets a save error about a control
        // that is no longer on screen to turn off.
        audio: { ...audio, copy: copyOn },
        compliance,
        facebook,
        backupIngestWanted,
      };
      // The stream key travels ONLY when this dialog is what changed it.
      //
      // The omitted-key merge that made the backup toggle one-way is the right
      // behaviour here, and it is load-bearing. internal/api/preannounce.go
      // writes a NEW primary key every time it creates a Facebook broadcast,
      // and states the invariant in its own words: the key the pre-created
      // broadcast returned has to be the one the encoder publishes to, or the
      // event page people were notified about stays empty beside a live stream.
      //
      // This dialog reads the key once, when it opens. An operator who opens
      // it, waits through a five-minute sweep, renames the destination and
      // saves was sending the key from before the sweep -- reverting a
      // pre-announced broadcast to a key nothing publishes to, with nothing on
      // screen saying so. Leaving the field out preserves whatever the row
      // holds now.
      //
      // Untouched is `streamKey === loadedStreamKey.current`, not "empty" and
      // not a trimmed comparison: an operator who types only whitespace has
      // still touched the field, and a create has no loaded value to be equal
      // to, so it always sends.
      if (!editing || streamKey !== loadedStreamKey.current) {
        payload.streamKey = streamKey.trim();
      }
      // The server drops settings this platform cannot send and says which.
      // Surfaced rather than swallowed: the case that produces them is
      // configuring a destination for one platform and then switching it, so
      // the operator set these on purpose and would otherwise watch a value
      // they chose disappear with no explanation.
      //
      // A separate toast from the success one, and after it, so the order
      // reads as "it saved, AND this happened to it".
      let warnings: string[] = [];
      if (editing) {
        ({ warnings = [] } = await api.updateDestination(destination.id, payload));
        toast.success("Destination updated.");
      } else {
        ({ warnings = [] } = await api.createDestination(payload));
        toast.success("Destination created. Set its audio routing next.");
      }
      for (const w of warnings) toast.warning(w, { duration: 10000 });
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
            This destination copies its video, and by default mixes and re-encodes its audio
            from its own routing profile. An SRT or file destination can copy the audio too,
            forwarding your encoder's tracks untouched. Put it on a rendition if the platform
            will not take the source video — a rendition re-encodes video once for everyone
            that shares it, and still leaves the audio to each destination.
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="dest-name">{t("dest.name")}</Label>
            <Input
              id="dest-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("dest.namePlaceholder")}
            />
          </div>

          <div className="flex flex-col gap-1">
            <Label>{t("dest.platform")}</Label>
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
                                {t("dest.urlFromDashboard")}
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
            <Label>{t("dest.transport")}</Label>
            <Select value={kind} onValueChange={(v) => setKind(v as DestKind)}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="rtmp">{t("dest.transportRtmp")}</SelectItem>
                <SelectItem value="srt">SRT</SelectItem>
                <SelectItem value="file">{t("dest.transportFile")}</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {showOAuth && caps?.connect && (
            <div className="flex flex-col gap-1">
              <Label>{t("dest.connectedAccount")}</Label>
              {platformAccounts.length > 0 ? (
                <Select value={accountId} onValueChange={setAccountId}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {/* "Not linked" is neutral wording on purpose. On Kick the
                        key is always typed, so calling the unlinked state
                        "manual" would imply the account link had failed. */}
                    <SelectItem value="none">{t("dest.notLinked")}</SelectItem>
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
              <Label htmlFor="dest-key">{t("dest.streamKeyLabel")}</Label>
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
          {/* Two cards, not a <Select>.
              A dropdown presents copying and encoding as the same kind of
              choice. They are not: copying is `-c:v copy` and costs nothing,
              while an encode is the most expensive thing an operator can switch
              on here. Making that a structural difference — two cards, with
              everything under the second one collapsed until it is chosen — is
              the whole point, and it is what Restreamer already does (its
              filter controls render only when the codec is not `copy`). */}
          <fieldset className="flex flex-col gap-2">
            <legend className="mb-1 text-[12px] font-medium">{t("dest.videoTreatment")}</legend>

            {/* role="radio" is only meaningful inside a radiogroup. Without this
                wrapper the two cards are announced as two unrelated radios with
                no shared name, so a screen reader user is told "checked" about a
                choice whose alternatives were never named. */}
            <div role="radiogroup" aria-label={t("dest.videoTreatment")} className="flex flex-col gap-2">
              <button
                type="button"
                role="radio"
                aria-checked={renditionId === PASSTHROUGH}
                onClick={() => setRenditionId(PASSTHROUGH)}
                className={cn(
                  "rounded-md border p-2.5 text-left transition-colors",
                  renditionId === PASSTHROUGH
                    ? "border-primary bg-primary/10"
                    : "border-border hover:border-primary/60",
                )}
              >
                <span className="flex items-center gap-2">
                  <StatusDot tone={renditionId === PASSTHROUGH ? "live" : "idle"} size="sm" />
                  <span className="text-[13px] font-medium">{t("dest.copySource")}</span>
                  <Badge variant="outline" className="ml-auto">recommended</Badge>
                </span>
                <span className="mt-1 block text-[10px] text-muted-foreground">
                  <span className="font-mono">-c:v copy</span> — the source video exactly as your
                  encoder sent it. No encode, no process, no CPU.
                </span>
              </button>

              <button
                type="button"
                role="radio"
                aria-checked={renditionId !== PASSTHROUGH}
                onClick={() => {
                  if (renditionId === PASSTHROUGH && renditions.length > 0) {
                    setRenditionId(String(renditions[0].rendition.id));
                  }
                }}
                disabled={renditions.length === 0}
                className={cn(
                  "rounded-md border p-2.5 text-left transition-colors disabled:opacity-50",
                  renditionId !== PASSTHROUGH
                    ? "border-primary bg-primary/10"
                    : "border-border hover:border-primary/60",
                )}
              >
                <span className="flex items-center gap-2">
                  <StatusDot tone={renditionId !== PASSTHROUGH ? "live" : "idle"} size="sm" />
                  <span className="text-[13px] font-medium">{t("dest.useSharedEncode")}</span>
                </span>
                <span className="mt-1 block text-[10px] text-muted-foreground">
                  {renditions.length === 0
                    ? "No shared encodes yet. Create one on the Renditions page first."
                    : "Changes the picture once and shares it between destinations."}
                </span>
              </button>
            </div>

            {/* Collapsed under Copy. The free path has nothing to configure,
                and rendering an empty picker under it would imply otherwise. */}
            {renditionId !== PASSTHROUGH && (
              <div className="ml-2 flex flex-col gap-1.5 border-l border-border pl-3">
                <Select value={renditionId} onValueChange={setRenditionId}>
                  {/* Named, because this control has no visible label of its
                      own — it sits under a radio card that supplies the meaning,
                      which leaves the select itself anonymous to a screen reader
                      and ambiguous to a test. */}
                  <SelectTrigger aria-label={t("dest.sharedEncodeAria")} data-testid="rendition-picker">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {renditions.map((v) => (
                      <SelectItem key={v.rendition.id} value={String(v.rendition.id)}>
                        {/* Spec first, name second. An encode named by its
                            creator tells the next person nothing; what it
                            PRODUCES tells them everything. */}
                        <span className="font-mono">{renditionSpec(v.rendition)}</span>
                        {v.rendition.name ? ` — ${v.rendition.name}` : ""}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>

                {selectedView && (
                  <span className="text-[10px] text-muted-foreground">
                    {/* The consequence, computed rather than asserted. Whether
                        this costs a new encode or nothing at all is the fact
                        the whole shared model turns on, and it was previously
                        one static sentence that could not tell the two apart. */}
                    {joinConsequence(selectedView)}
                  </span>
                )}
                {selectedRendition?.note && (
                  <span className="text-[10px] text-muted-foreground">{selectedRendition.note}</span>
                )}

                {/* Advanced: make a variant.
                    There is no per-destination override of a shared encode, and
                    there must not be — editing the shared tier would silently
                    change the picture every other destination on it receives.
                    So "customise for this destination" means creating a second
                    encode, and the cost is stated before the form, not after
                    the operator has filled it in. */}
                {selectedRendition && (
                  <details
                    className="mt-1 rounded-md border border-border p-2"
                    open={variantOpen}
                    onToggle={(e) => setVariantOpen((e.target as HTMLDetailsElement).open)}
                  >
                    <summary className="cursor-pointer text-[11px] font-medium">
                      {t("dest.customiseSummary")}
                    </summary>
                    <p className="mt-1.5 text-[10px] text-muted-foreground">
                      {t("dest.variantWarning", { name: selectedRendition.name })}
                    </p>
                    <div className="mt-2 grid grid-cols-2 gap-2">
                      <div className="flex flex-col gap-1">
                        <Label htmlFor="var-bitrate">{t("dest.varBitrate")}</Label>
                        <Input
                          id="var-bitrate"
                          value={variantBitrate}
                          onChange={(e) => setVariantBitrate(e.target.value)}
                          inputMode="numeric"
                        />
                      </div>
                      <div className="flex flex-col gap-1">
                        <Label htmlFor="var-fps">{t("dest.varFps")}</Label>
                        <Input
                          id="var-fps"
                          value={variantFps}
                          onChange={(e) => setVariantFps(e.target.value)}
                          inputMode="numeric"
                          placeholder={t("dest.varSourceHint")}
                        />
                      </div>
                      <div className="flex flex-col gap-1">
                        <Label htmlFor="var-width">{t("dest.varWidth")}</Label>
                        <Input
                          id="var-width"
                          value={variantWidth}
                          onChange={(e) => setVariantWidth(e.target.value)}
                          inputMode="numeric"
                          placeholder={t("dest.varSourceHint")}
                        />
                      </div>
                      <div className="flex flex-col gap-1">
                        <Label htmlFor="var-height">{t("dest.varHeight")}</Label>
                        <Input
                          id="var-height"
                          value={variantHeight}
                          onChange={(e) => setVariantHeight(e.target.value)}
                          inputMode="numeric"
                          placeholder={t("dest.varSourceHint")}
                        />
                      </div>
                    </div>
                    {/* One click to the platform's own numbers. The case this
                        exists for is "make this fit Kick", and making someone
                        transcribe four figures from the panel above into the
                        four fields below is how they end up mistyping one. */}
                    {platformVideo && (
                      <button
                        type="button"
                        className="mt-2 text-left text-[10px] text-primary underline hover:opacity-80"
                        onClick={() => {
                          // The TOP of the range, not the bottom.
                          //
                          // Seeding kbpsMin looked reasonable and was wrong:
                          // Kick publishes 1000-8000, and 1000 kbps at 1080p60
                          // is a picture nobody would ship. Where a platform
                          // gives a range it is a range of what it ACCEPTS, and
                          // the useful end is the one that looks best — the
                          // operator can lower it, and the field is right there.
                          const kbps = platformVideo.kbpsMax || platformVideo.kbpsMin;
                          if (kbps) setVariantBitrate(String(kbps));
                          if (platformVideo.width) setVariantWidth(String(platformVideo.width));
                          if (platformVideo.height) setVariantHeight(String(platformVideo.height));
                          if (platformVideo.fps) setVariantFps(String(platformVideo.fps));
                          setVariantErr("");
                        }}
                      >
                        {t("dest.fillFromPlatform")}
                      </button>
                    )}
                    {variantErr && (
                      <p className="mt-1.5 text-[10px] text-down">{variantErr}</p>
                    )}
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      className="mt-2"
                      disabled={busy}
                      onClick={async () => {
                        setVariantErr("");
                        const kbps = Number(variantBitrate);
                        if (!Number.isFinite(kbps) || kbps <= 0) {
                          // Every other field may legitimately be blank ("keep
                          // the source's"). Bitrate cannot: an encode with no
                          // target is not a variation of anything.
                          setVariantErr("Video bitrate is required — an encode needs a target.");
                          return;
                        }
                        setBusy(true);
                        try {
                          const num = (v: string) => (v.trim() === "" ? 0 : Number(v));
                          const made = await api.createRendition({
                            ...selectedRendition,
                            id: undefined,
                            // Named after what it is FOR, so the picker's
                            // spec-first row is followed by something that says
                            // why this one exists rather than repeating the spec.
                            name: `${selectedRendition.name} — ${name || "variant"}`,
                            videoBitrate: kbps,
                            width: num(variantWidth),
                            height: num(variantHeight),
                            fps: num(variantFps),
                          } as Partial<Rendition>);
                          const rows = await api.listRenditions();
                          setRenditions(rows);
                          setRenditionId(String(made.rendition.id));
                          setVariantOpen(false);
                        } catch (e) {
                          setVariantErr(e instanceof Error ? e.message : "Could not create the encode.");
                        } finally {
                          setBusy(false);
                        }
                      }}
                    >
                      {t("dest.createVariant")}
                    </Button>
                  </details>
                )}
              </div>
            )}

            {/* What the platform asks for, with its provenance attached.
                Shown under BOTH cards rather than only under the encode one:
                an operator on passthrough needs to know their 4K source is
                above what Kick will take just as much as one who is already
                encoding — arguably more, since nothing is going to reshape it
                for them. */}
            {platformVideo && (
              <div className="rounded-md border border-border bg-background p-2">
                <span className="flex flex-wrap items-baseline gap-x-2">
                  <span className="text-[11px] font-medium">
                    {guidance.find((g) => g.id === presetId)?.name ?? "This platform"} publishes:
                  </span>
                  <span className="font-mono text-[10px] text-muted-foreground">
                    {[
                      platformVideo.width && platformVideo.height
                        ? `${platformVideo.width}×${platformVideo.height}`
                        : null,
                      platformVideo.fps ? `${platformVideo.fps} fps` : null,
                      platformVideo.kbpsMin
                        ? platformVideo.kbpsMax && platformVideo.kbpsMax !== platformVideo.kbpsMin
                          ? `${platformVideo.kbpsMin}–${platformVideo.kbpsMax} kbps`
                          : `${platformVideo.kbpsMin} kbps`
                        : null,
                      platformVideo.gopSeconds ? `${platformVideo.gopSeconds}s keyframes` : null,
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                  </span>
                </span>
                {platformVideo.note && (
                  <p className="mt-1 text-[10px] leading-relaxed text-muted-foreground">
                    {platformVideo.note}
                  </p>
                )}
                {/* The disclaimer this catalogue has always carried, applied to
                    numbers rather than to hostnames. A figure whose provenance
                    is not on screen is indistinguishable from a guess. */}
                <p className="mt-1 text-[10px] text-subtle-foreground">
                  Starting point, not a rule —{" "}
                  <a
                    href={platformVideo.source}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="underline hover:text-foreground"
                  >
                    published by the platform
                  </a>
                  , read {platformVideo.checked}. Verify before you go live.
                </p>
              </div>
            )}

            {leaving && (
              <span className="text-[10px] text-warn">
                {leaveConsequence(leaving)}
              </span>
            )}
          </fieldset>

          {canCopyAudio && (
            <div className="flex flex-col gap-1">
              <Label htmlFor="dest-audio-copy">Audio</Label>
              <div className="flex h-9 items-center gap-2">
                <Switch
                  id="dest-audio-copy"
                  checked={copyOn}
                  onCheckedChange={(v) => setAudio({ ...audio, copy: v })}
                />
                <span className="text-[11px] text-muted-foreground">
                  {copyOn ? "Copy the ingest tracks" : "Mix and re-encode"}
                </span>
              </div>
              <span className="text-[10px] text-muted-foreground">
                {copyOn
                  ? "This destination forwards your encoder's own audio tracks untouched — no decode, no mix, no encoder. Which tracks go out, and which roles are excluded, still apply. Everything the mix does to the samples does not: gain, normalization, loudness, ducking, delay and mono are all refused on save rather than silently ignored."
                  : "The mix path: your routing profile decides the tracks and levels, and the result is encoded to one stereo track. Switch to copy for an archive or a hand-off where the receiver can take the tracks as they are."}
              </span>
            </div>
          )}

          <div className="flex flex-col gap-1">
            <Label htmlFor="dest-bitrate">{t("dest.audioBitrate")}</Label>
            <div className="flex items-center gap-2">
              <Input
                id="dest-bitrate"
                type="number"
                min={32}
                max={512}
                value={bitrate}
                onChange={(e) => setBitrate(Number(e.target.value))}
                className="w-24"
                disabled={copyOn}
              />
              <span className="text-[11px] text-muted-foreground">kbps, AAC stereo</span>
            </div>
            <span className="text-[10px] text-muted-foreground">
              {copyOn
                ? "Not used: nothing is encoded, so the bitrate is whatever your encoder sent."
                : "Mixed here from this destination's own routing profile, whichever rendition it is on — a rendition never touches audio."}
            </span>
          </div>

          <div className="grid grid-cols-2 gap-2">
            <div className="flex flex-col gap-1">
              <Label>{t("dest.audioCodec")}</Label>
              <Select
                value={audio.codec || "aac"}
                onValueChange={(v) =>
                  setAudio({ ...audio, codec: v === "aac" ? "" : (v as "opus") })
                }
                disabled={kind === "rtmp" || copyOn}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="aac">AAC — every platform takes it</SelectItem>
                  <SelectItem value="opus">Opus — better below 64 kbps</SelectItem>
                </SelectContent>
              </Select>
              <span className="text-[10px] text-muted-foreground">
                {copyOn
                  ? "Not used: nothing is encoded, so the codec is whatever your encoder sent."
                  : kind === "rtmp"
                    ? "RTMP is AAC. FFmpeg will mux Opus into FLV, but no mainstream ingest accepts it — the stream would upload cleanly and be rejected."
                    : "Opus is worth it for a low-bitrate feed. Check the receiver takes it."}
              </span>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="dest-mono">{t("dest.channels")}</Label>
              <div className="flex h-9 items-center gap-2">
                <Switch
                  id="dest-mono"
                  checked={audio.mono ?? false}
                  onCheckedChange={(v) => setAudio({ ...audio, mono: v })}
                  disabled={copyOn}
                />
                <span className="text-[11px] text-muted-foreground">
                  {audio.mono ? "Mono" : "Stereo"}
                </span>
              </div>
              <span className="text-[10px] text-muted-foreground">
                {copyOn
                  ? "Not used: folding to one channel needs a decode and a re-encode, which is what copy removes."
                  : "Mono folds your mix down rather than re-routing it, and halves the bitrate on talk content for no perceptual loss."}
              </span>
            </div>
          </div>

          {(platform === "youtube" || platform === "twitch" || platform === "facebook") && (
            <div className="flex flex-col gap-3 rounded-md border border-warn/40 bg-warn/5 p-2">
              <p className="text-xs font-medium">{t("dest.compliance")}</p>
              <span className="text-[10px] text-muted-foreground">
                Not cosmetic. COPPA is a law for YouTube, Twitch requires labels for several
                content classes, Facebook has no way to widen a broadcast's audience once
                someone has already seen it, and going live publicly by accident cannot be
                undone. Anything left unset is not sent at all &mdash; polyemesis never
                overwrites a setting you did not choose.
              </span>

              {platform === "youtube" && (
                <>
                  <div className="flex flex-col gap-1">
                    <Label>{t("dest.visibility")}</Label>
                    <Select
                      value={compliance.privacy || "unset"}
                      onValueChange={(v) =>
                        setCompliance({
                          ...compliance,
                          privacy: v === "unset" ? "" : (v as PrivacyStatus),
                        })
                      }
                    >
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="unset">{t("dest.leaveOnYouTube")}</SelectItem>
                        <SelectItem value="private">{t("dest.private")}</SelectItem>
                        <SelectItem value="unlisted">{t("dest.unlisted")}</SelectItem>
                        <SelectItem value="public">{t("dest.public")}</SelectItem>
                      </SelectContent>
                    </Select>
                    <span className="text-[10px] text-muted-foreground">
                      "Leave as it is" is the safe default and means no visibility write happens at
                      all. That matters: YouTube's update is destructive by section, so a write
                      that omitted this would revert your broadcast to its default rather than
                      leaving it alone.
                    </span>
                  </div>

                  <div className="flex flex-col gap-1">
                    <Label>{t("dest.madeForKids")}</Label>
                    <Select
                      value={
                        compliance.madeForKids === undefined ||
                        compliance.madeForKids === null
                          ? "unset"
                          : compliance.madeForKids
                            ? "yes"
                            : "no"
                      }
                      // null, not undefined, for the same reason the backup
                      // toggle sends a bare boolean: undefined is omitted from
                      // the body and the server decodes over the stored row, so
                      // going back to "leave as it is" silently kept whichever
                      // declaration was there. db.Compliance.MadeForKids is a
                      // *bool precisely so that null is expressible.
                      onValueChange={(v) =>
                        setCompliance({
                          ...compliance,
                          madeForKids: v === "unset" ? null : v === "yes",
                        })
                      }
                    >
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="unset">{t("dest.leaveOnYouTube")}</SelectItem>
                        <SelectItem value="no">No &mdash; not made for kids</SelectItem>
                        <SelectItem value="yes">Yes &mdash; made for kids</SelectItem>
                      </SelectContent>
                    </Select>
                    <span className="text-[10px] text-muted-foreground">
                      "No" is a declaration, not the absence of one, so it is sent. This is written
                      through YouTube's video resource rather than the broadcast, because that is
                      the only place it is settable once a broadcast exists.
                    </span>
                  </div>
                </>
              )}

              {platform === "twitch" && (
                <div className="flex flex-col gap-1">
                  <Label>{t("dest.contentLabels")}</Label>
                  <div className="flex flex-col gap-1">
                    {TWITCH_LABELS.map((id) => (
                      <div key={id} className="flex items-center justify-between gap-2">
                        <span className="text-[11px]">{id.replace(/([A-Z])/g, " $1").trim()}</span>
                        <Switch
                          checked={compliance.labels?.[id] ?? false}
                          onCheckedChange={(v) =>
                            setCompliance({
                              ...compliance,
                              labels: { ...compliance.labels, [id]: v },
                            })
                          }
                        />
                      </div>
                    ))}
                  </div>
                  <span className="text-[10px] text-muted-foreground">
                    Twitch requires these for mature games, sexual themes, drugs, gambling and
                    graphic violence. "Mature game" is not here because Twitch derives it from the
                    category and refuses to let anything set it.
                  </span>
                </div>
              )}

              {platform === "facebook" && facebookTargetsAPage && (
                <span className="text-[10px] text-muted-foreground">
                  This account publishes to a Page, and a Page broadcast is public: Facebook
                  has no personal audience to restrict it to. There is no audience setting to
                  make, which is why one is not offered.
                </span>
              )}

              {platform === "facebook" && !facebookTargetsAPage && (
                <div className="flex flex-col gap-1">
                  <Label>{t("dest.audience")}</Label>
                  <Select
                    value={compliance.facebookPrivacy || "unset"}
                    onValueChange={(v) =>
                      setCompliance({
                        ...compliance,
                        facebookPrivacy: v === "unset" ? "" : v,
                      })
                    }
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="unset">{t("dest.leaveOnFacebook")}</SelectItem>
                      <SelectItem value="SELF">{t("dest.onlyMe")}</SelectItem>
                      <SelectItem value="ALL_FRIENDS">{t("dest.friends")}</SelectItem>
                      <SelectItem value="FRIENDS_OF_FRIENDS">{t("dest.friendsOfFriends")}</SelectItem>
                      <SelectItem value="EVERYONE">{t("dest.public")}</SelectItem>
                    </SelectContent>
                  </Select>
                  <span className="text-[10px] text-muted-foreground">
                    Applied when the broadcast is CREATED, not while it airs &mdash; changing this
                    afterwards takes effect on the next broadcast, not the current one. If this
                    destination is later pointed at a Page, the setting stops applying: a Page
                    broadcast is public regardless.
                  </span>
                </div>
              )}
            </div>
          )}

          {/* Not compliance: neither field is an obligation, and both are
              create-time-only, same as Audience above -- so this is its own
              box rather than folded into the amber one. See db.FacebookSettings. */}
          {platform === "facebook" && (
            <div className="flex flex-col gap-3 rounded-md border border-border p-2">
              <p className="text-xs font-medium">{t("dest.fbBoxTitle")}</p>
              <span className="text-[10px] text-muted-foreground">{t("dest.fbBoxIntro")}</span>

              {/* Both costs stated, because both are real and neither is
                  guessable. The reconnect is unavoidable rather than sloppy: a
                  backup endpoint exists only on a broadcast created with one,
                  and creating one issues a new primary key. */}
              <div className="flex items-start gap-2">
                <input
                  id="fb-backup-ingest"
                  type="checkbox"
                  className="mt-0.5"
                  checked={backupIngestWanted}
                  // The bare boolean, never `checked || undefined`. The PUT is
                  // decoded OVER the stored row, so a key JSON.stringify omits
                  // is a key the server leaves exactly as it was: unchecking
                  // the box used to send nothing, the stored `true` survived,
                  // and the dialog said it had saved while the backup feed kept
                  // running at double the upload the help text below warns
                  // about. `false` has to travel for "off" to mean anything.
                  //
                  // A top-level setter, not part of `facebook`: the intent now
                  // lives on the destination beside the endpoint it gates, so
                  // any platform that offers a backup endpoint can use the same
                  // field and the same engine path. Rendered here because
                  // Facebook is the only one that offers one today.
                  onChange={(e) => setBackupIngestWanted(e.target.checked)}
                />
                <div className="flex flex-col gap-0.5">
                  <Label htmlFor="fb-backup-ingest">{t("dest.fbBackupLabel")}</Label>
                  {/* Three keys rather than one sentence with markup in it. The
                      cost is the half that has to survive a skim, so it stays
                      emphasised -- and a translator given a sentence fragment
                      ending in a comma cannot reorder it into their own grammar,
                      which is how emphasis markup usually ruins a catalogue. */}
                  <span className="text-[10px] text-muted-foreground">
                    {t("dest.fbBackupHelp")} <strong>{t("dest.fbBackupCost")}</strong>{" "}
                    {t("dest.fbBackupReconnect")}
                  </span>
                </div>
              </div>

              <div className="flex flex-col gap-2">
                <Label>{t("dest.fbCrosspostLabel")}</Label>
                {/* `target`, not `t`: the translator is called inside this map
                    and a one-letter row variable shadowed it. */}
                {(facebook.crosspost ?? []).map((target, i) => (
                  <div key={i} className="flex items-center gap-2">
                    <Input
                      value={target.pageId}
                      onChange={(e) => {
                        const next = [...(facebook.crosspost ?? [])];
                        next[i] = { ...next[i], pageId: e.target.value };
                        setFacebook({ ...facebook, crosspost: next });
                      }}
                      placeholder={t("dest.fbCrosspostPageId")}
                      className="flex-1 font-mono"
                    />
                    <div className="flex items-center gap-1.5">
                      <Switch
                        id={`dest-fb-crosspost-post-${i}`}
                        checked={target.createPost ?? false}
                        onCheckedChange={(v) => {
                          const next = [...(facebook.crosspost ?? [])];
                          next[i] = { ...next[i], createPost: v };
                          setFacebook({ ...facebook, crosspost: next });
                        }}
                      />
                      <Label htmlFor={`dest-fb-crosspost-post-${i}`} className="font-normal">
                        {t("dest.fbCrosspostAlsoPost")}
                      </Label>
                    </div>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      aria-label={t("dest.fbCrosspostRemove")}
                      onClick={() => {
                        const next = (facebook.crosspost ?? []).filter((_, j) => j !== i);
                        setFacebook({ ...facebook, crosspost: next });
                      }}
                    >
                      <Trash2 className="size-3.5" />
                    </Button>
                  </div>
                ))}
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() =>
                    setFacebook({
                      ...facebook,
                      crosspost: [...(facebook.crosspost ?? []), { pageId: "" }],
                    })
                  }
                >
                  <Plus className="size-3.5" /> {t("dest.fbCrosspostAdd")}
                </Button>
                <span className="text-[10px] text-muted-foreground">
                  {t("dest.fbCrosspostHelp")}
                </span>
              </div>

              <div className="flex flex-col gap-1">
                <Label htmlFor="dest-fb-donate">{t("dest.fbDonateLabel")}</Label>
                <Input
                  id="dest-fb-donate"
                  value={facebook.donateCharityId ?? ""}
                  onChange={(e) =>
                    setFacebook({ ...facebook, donateCharityId: e.target.value })
                  }
                  placeholder={t("dest.fbDonatePlaceholder")}
                  className="font-mono"
                />
                <span className="text-[10px] text-muted-foreground">
                  {t("dest.fbDonateHelp")}
                </span>
              </div>
            </div>
          )}

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
                <Label htmlFor="dest-rwt">{t("dest.socketTimeout")}</Label>
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
                  <Label htmlFor="dest-mqp">{t("dest.muxQueue")}</Label>
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
                {t("dest.muxQueuePair")}
              </span>

              <div className="border-t border-border pt-3">
                <p className="text-xs font-medium">{t("dest.reconnecting")}</p>
              </div>

              <div className="grid grid-cols-2 gap-2">
                <div className="flex flex-col gap-1">
                  <Label htmlFor="dest-minbo">{t("dest.retryAfter")}</Label>
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
                <Label htmlFor="dest-giveup">{t("dest.giveUpAfter")}</Label>
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
