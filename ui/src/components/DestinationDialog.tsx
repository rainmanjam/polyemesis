import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router";
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
import { Experimental, ExperimentalBadge } from "@/components/Experimental";
import { StatusDot } from "@/components/signature/StatusDot";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { SecretInput } from "@/components/SecretInput";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { api, isNoSource } from "@/lib/api";
import {
  initialSourceValue,
  sourceChoices,
  sourceIdForSave,
  sourceIsChosen,
} from "@/lib/destinationSource";
import { FACEBOOK_PRIVACIES } from "@/lib/facebookPrivacy";
import { useT, useTN, type TranslationKey, type Translator } from "@/lib/i18n";
import { DEST_STEPS, stepForError, type DestStep } from "@/lib/destinationSteps";
import { isIpLiteralHost, platformSupportsDeviceCode } from "@/lib/platformConnect";
import { DeviceCodeDialog } from "@/components/DeviceCodeDialog";
import { InfoHint } from "@/components/InfoHint";
import {
  failedRead,
  mayClaim,
  okRead,
  pendingRead,
  readFailed,
  rowsOf,
  type ReadState,
} from "@/lib/readState";
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
  TlsMode,
  PlatformCreds,
  PlatformPresetInfo,
  SetupGuide,
  ServiceInfo,
  RenditionView,
  SourceView,
  RenditionConcern,
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
  notes: TranslationKey;
  /** Extra search terms, because the name an operator types is not always the
   *  name on the tin. */
  aliases?: string[];
}

/** Shown beside the catalogue, always. Ingest hostnames and platform limits
 *  move without notice, so no preset is presented as authoritative. */
const PRESET_DISCLAIMER: TranslationKey = "addDest.presetDisclaimer";

const PRESET_GROUPS: { key: PresetGroupKey; label: TranslationKey }[] = [
  { key: "major", label: "addDest.groupMajor" },
  { key: "video", label: "addDest.groupVideo" },
  { key: "selfhosted", label: "addDest.groupSelfHosted" },
  { key: "cloud", label: "addDest.groupCloud" },
  { key: "generic", label: "addDest.groupGeneric" },
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
    notes: "addDest.presetNote.youtube",
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
    notes: "addDest.presetNote.youtubeRtmps",
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
    notes: "addDest.presetNote.twitch",
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
    notes: "addDest.presetNote.twitchRtmps",
    aliases: ["ttv", "tls"],
  },
  {
    id: "kick",
    name: "Kick",
    group: "major",
    transport: "rtmps",
    kind: "rtmp",
    platform: "kick",
    // Empty on purpose — see the matching comment in internal/db/platforms.go.
    // Kick's host is per channel, so a prefill is somebody else's address, and
    // the shape Kick's dashboard prints has no application path.
    url: "",
    separateKey: true,
    helpUrl: "https://kick.com/dashboard/settings/stream",
    notes: "addDest.presetNote.kick",
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
    notes: "addDest.presetNote.facebook",
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
    notes: "addDest.presetNote.instagram",
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
    notes: "addDest.presetNote.tiktok",
  },

  {
    id: "x",
    name: "X (Twitter) Live",
    group: "video",
    transport: "rtmps",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes: "addDest.presetNote.x",
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
    notes: "addDest.presetNote.linkedin",
  },
  {
    id: "trovo",
    name: "Trovo",
    group: "video",
    transport: "rtmp",
    kind: "rtmp",
    platform: "trovo",
    url: "",
    separateKey: true,
    notes: "addDest.presetNote.trovo",
  },
  {
    id: "dlive",
    name: "DLive",
    group: "video",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes: "addDest.presetNote.dlive",
  },
  {
    id: "rumble",
    name: "Rumble",
    group: "video",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes: "addDest.presetNote.rumble",
  },
  {
    id: "odysee",
    name: "Odysee",
    group: "video",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes: "addDest.presetNote.odysee",
    aliases: ["lbry"],
  },
  {
    id: "vimeo",
    name: "Vimeo Livestream",
    group: "video",
    transport: "rtmps",
    kind: "rtmp",
    // Named, and the key is STILL pasted — the first preset here where those
    // two come apart. The platform carries sign-in and the live-API
    // entitlement probe; separateKey stays because Vimeo issues the ingest per
    // live event and creating one is Enterprise-only. See
    // internal/oauth/vimeo.go before "finishing" this by clearing separateKey.
    platform: "vimeo",
    url: "",
    separateKey: true,
    helpUrl: "https://developer.vimeo.com/api/reference/live",
    notes: "addDest.presetNote.vimeo",
  },
  {
    id: "dailymotion",
    name: "Dailymotion",
    group: "video",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes: "addDest.presetNote.dailymotion",
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
    notes: "addDest.presetNote.peertube",
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
    notes: "addDest.presetNote.owncast",
  },
  {
    id: "wowza-engine",
    name: "Wowza Streaming Engine (self-hosted)",
    group: "selfhosted",
    transport: "rtmp",
    kind: "rtmp",
    url: "rtmp://{wowza-host}:1935/{application}",
    separateKey: true,
    notes: "addDest.presetNote.wowzaEngine",
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
    notes: "addDest.presetNote.cloudflareStream",
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
    notes: "addDest.presetNote.mux",
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
    notes: "addDest.presetNote.awsIvs",
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
    notes: "addDest.presetNote.restream",
  },
  {
    id: "castr",
    name: "Castr",
    group: "cloud",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes: "addDest.presetNote.castr",
  },
  {
    id: "livepush",
    name: "Livepush",
    group: "cloud",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes: "addDest.presetNote.livepush",
  },
  {
    id: "boxcast",
    name: "BoxCast",
    group: "cloud",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes: "addDest.presetNote.boxcast",
  },
  {
    id: "wowza-video",
    name: "Wowza Video (hosted)",
    group: "cloud",
    transport: "rtmps",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes: "addDest.presetNote.wowzaVideo",
  },
  {
    id: "akamai-msl",
    name: "Akamai Media Services Live",
    group: "cloud",
    transport: "rtmp",
    kind: "rtmp",
    url: "",
    separateKey: true,
    notes: "addDest.presetNote.akamaiMsl",
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
    notes: "addDest.presetNote.azureMedia",
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
    notes: "addDest.presetNote.genericRtmp",
  },
  {
    id: "generic-rtmps",
    name: "Generic RTMPS",
    group: "generic",
    transport: "rtmps",
    kind: "rtmp",
    url: "rtmps://{host}:443/{application}",
    separateKey: true,
    notes: "addDest.presetNote.genericRtmps",
  },
  {
    id: "generic-srt",
    name: "Generic SRT",
    group: "generic",
    transport: "srt",
    kind: "srt",
    url: "srt://{host}:{port}?streamid={streamid}",
    separateKey: false,
    notes: "addDest.presetNote.genericSrt",
  },
  {
    id: "generic-hls",
    name: "Generic HLS push",
    group: "generic",
    transport: "hls",
    url: "",
    separateKey: false,
    notes: "addDest.presetNote.genericHls",
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
function renditionSpec(r: Rendition, t: Translator): string {
  const scaled = r.width > 0 || r.height > 0;
  // 0 on an axis means "derive it from the source", which reads as "auto"
  // rather than as a zero the user is being asked to believe in.
  const auto = t("lib.autoBadge");
  const size = scaled ? `${r.width || auto}×${r.height || auto}` : t("rend.sizeSource");
  const fps = r.fps > 0 ? `${r.fps} fps` : t("rend.sourceFps");
  return `${size} · ${fps} · ${r.videoBitrate} kbps · ${r.encoder}`;
}

/** kindCarriesStreamKey mirrors db.DestKind.CarriesStreamKey: only RTMP joins a
 *  stream key onto the publish URL. */
const kindCarriesStreamKey = (k: DestKind) => k === "rtmp";

/**
 * keyAfterKindChange is what the stream key becomes when the transport changes.
 *
 * THE FIELD IS CLEARED ON THE WAY OUT OF RTMP, AND THAT IS THE POINT OF #610.
 * Retyping a destination to srt, file or audio used to leave the key on the row:
 * the field renders only for RTMP, so there was no screen left to clear it on,
 * and save omits an untouched key, so nothing sent a clear either. The result
 * was a destination publishing to the bare URL WITH NO CREDENTIAL, and a live
 * key sealed where nobody could see it — so it could not be rotated, and
 * GET /destinations went on returning it in full.
 *
 * The server now REFUSES that combination. Clearing here is what keeps the
 * refusal off the operator's screen: they cannot reach the state the validator
 * rejects, so they never meet an error about a field the form does not show.
 *
 * ONLY ON AN ACTUAL CHANGE. Re-picking the kind that is already selected must
 * not wipe the key of an RTMP destination that is simply being edited, and it
 * is the preset picker that makes this more than theory — choosing an RTMP
 * preset calls this with the kind unchanged, halfway through configuring a
 * destination whose key is already typed.
 */
export function keyAfterKindChange(from: DestKind, to: DestKind, key: string): string {
  if (to === from) return key;
  return kindCarriesStreamKey(to) ? key : "";
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
  // Every operator-visible sentence here comes from the catalogue. The preset
  // table stores keys rather than English, so its notes and group labels are
  // translated at render; preset names stay as the platforms spell them.
  const t = useT();
  const tn = useTN();

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
  const changeKind = (next: DestKind) => {
    setStreamKey((k) => keyAfterKindChange(kind, next, k));
    setKind(next);
  };
  const [bitrate, setBitrate] = useState(160);
  // Muxer and socket tuning. An empty object is "no opt-in", which is what
  // every destination that predates this carries, and what the server turns
  // into no FFmpeg arguments at all.
  const [transport, setTransport] = useState<DestTransport>({});
  // Reconnect policy. Empty is "retry forever, 1s to 30s", which is what every
  // destination did before this existed.
  const [resilience, setResilience] = useState<DestResilience>({});
  // Broadcast delay in seconds (#11). 0 is off. The loaded value is kept so
  // the dialog can say, before the save, that changing it restarts a live
  // destination.
  const [delaySeconds, setDelaySeconds] = useState(0);
  const [loadedDelay, setLoadedDelay] = useState(0);
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
  // "Negotiate Enhanced Broadcasting with Twitch at go-live." Top-level and
  // platform-neutral in the row, the same way backupIngestWanted is, but the
  // control is rendered only in the Twitch box because Twitch is the one
  // platform that publishes this negotiation today — see
  // db.Destination.Multitrack.
  const [multitrack, setMultitrack] = useState(false);
  const [accountId, setAccountId] = useState<string>("none");
  // READ STATES, NOT BARE ARRAYS. #719.
  //
  // These were `.then(setX).catch(() => setX([]))`, which stores a FAILED read
  // as the same value as a successful empty one -- and this dialog turns both
  // into positive claims that drive the operator to act: "No shared encodes
  // yet. Create one on the Renditions page first." sends them to build a second
  // real encode they already have, and a missing account list replaces the
  // picker with "Connect account", a re-authorisation for an account that is
  // already linked which also discards the half-filled dialog.
  //
  // lib/readState exists to make that unwriteable and this file never imported
  // it. The arrays below are derived with rowsOf, so every consumer that only
  // wants rows is unchanged; mayClaim guards the two places that say something.
  const [accountsRead, setAccountsRead] = useState<ReadState<PlatformAccount[]>>(pendingRead);
  // The setup guides say which platforms can sign in with a code instead of
  // a redirect. Only those get the in-place code path.
  const [guidesRead, setGuidesRead] = useState<ReadState<SetupGuide[]>>(pendingRead);
  const [deviceOpen, setDeviceOpen] = useState(false);
  // The server refuses a device start without this platform's developer
  // credentials, so the code path is offered only when they are stored. The
  // same rule as Settings' platformConnectControls.
  const [credsRead, setCredsRead] = useState<ReadState<PlatformCreds[]>>(pendingRead);
  const accounts = rowsOf(accountsRead);
  const [renditionId, setRenditionId] = useState<string>(PASSTHROUGH);
  // RenditionView, not Rendition. The view carries `destinations` and
  // `enabledDestinations`, and this used to strip them off one line after they
  // arrived — throwing away the only data that can tell an operator whether
  // picking an encode starts a new one or joins a running one. That fact is
  // the entire argument for renditions existing, and the UI could not state it.
  const [renditionsRead, setRenditionsRead] = useState<ReadState<RenditionView[]>>(pendingRead);
  const renditions = rowsOf(renditionsRead);
  /** #661: how the chosen rendition sits against the chosen platform's published
   *  figures. Fetched rather than computed — the comparison reads researched,
   *  dated numbers out of internal/db/platforms.go, and a second copy here would
   *  drift from that file exactly as the marketing site's hand-copied figures
   *  once did. */
  const [concerns, setConcerns] = useState<RenditionConcern[]>([]);
  const [sourcesRead, setSourcesRead] = useState<ReadState<SourceView[]>>(pendingRead);
  const sources = rowsOf(sourcesRead);
  // Which programme this destination carries. A string because that is what the
  // Select speaks; "" means nobody has chosen yet, which is a state the save
  // button reads rather than a value it sends.
  const [sourceId, setSourceId] = useState<string>("");
  const choices = useMemo(() => sourceChoices(sources), [sources]);
  // The starting value has to wait for the list, because what it should be
  // DEPENDS on the list: the only programme when there is one, and deliberately
  // nothing when there are several. Applied only while nothing has been chosen,
  // so it cannot overwrite an operator mid-edit or reset an existing
  // destination's programme when the fetch resolves late.
  useEffect(() => {
    if (!open || choices.length === 0) return;
    setSourceId((current) => (current === "" ? initialSourceValue(destination, choices) : current));
  }, [open, choices, destination]);
  const [guidanceRead, setGuidanceRead] = useState<ReadState<PlatformPresetInfo[]>>(pendingRead);
  const guidance = rowsOf(guidanceRead);
  const [servicesRead, setServicesRead] = useState<ReadState<ServiceInfo[]>>(pendingRead);
  const services = rowsOf(servicesRead);

  /** The registry entry for whatever platform is selected, or null for a
   *  custom destination — which the registry has no opinion about, and must
   *  not acquire one. */
  const service = useMemo(
    () => services.find((sv) => sv.id === platform) ?? null,
    [services, platform],
  );

  /** Whether the typed URL is one of the platform's own published servers.
   *  "no" is not an error: Twitch's regional hosts change, and an operator
   *  with a newer address than our copy is right and we are stale. */
  const urlIsAPublishedServer = useMemo(
    () => !!service?.servers?.some((sv) => sv.url === url),
    [service, url],
  );
  const [busy, setBusy] = useState(false);
  // Which step of the dialog is showing (A6), and the reason Save sent the
  // operator back to one. Every step stays mounted, so moving between them
  // keeps what was typed.
  const [step, setStep] = useState<DestStep>(1);
  const [stepError, setStepError] = useState<string | null>(null);
  const [tlsMode, setTlsMode] = useState<TlsMode | null>(null);
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

  // Re-read after an account is connected in place, so the picker shows it
  // without the dialog being closed and its half-filled form lost.
  const reloadAccounts = useCallback(() => {
    api
      .listAccounts()
      .then((r) => setAccountsRead(okRead(r)))
      .catch(() => setAccountsRead(failedRead()));
  }, []);

  useEffect(() => {
    if (!open) return;
    // Every open starts at the first step with nothing to complain about.
    setStep(1);
    setStepError(null);
    api.tlsStatus().then(
      (tls) => setTlsMode(tls.mode),
      () => setTlsMode(null),
    );
    reloadAccounts();
    api
      .platformGuides()
      .then((r) => setGuidesRead(okRead(r)))
      .catch(() => setGuidesRead(failedRead()));
    api
      .listCreds()
      .then((r) => setCredsRead(okRead(r)))
      .catch(() => setCredsRead(failedRead()));
    api
      .listRenditions()
      .then((r) => setRenditionsRead(okRead(r)))
      .catch(() => setRenditionsRead(failedRead()));
    api
      .listSources()
      .then((r) => setSourcesRead(okRead(r)))
      .catch(() => setSourcesRead(failedRead()));
    // Fetched, not mirrored. The UI keeps its own preset list so the picker
    // renders before any request resolves, but the researched numbers carry a
    // source and a date and a second copy of them here would drift silently —
    // which is exactly what happened when they were added to the Go catalogue
    // and nothing surfaced them.
    api
      .platformPresets()
      .then((r) => setGuidanceRead(okRead(r.presets)))
      .catch(() => setGuidanceRead(failedRead()));
    // The ingest servers and the platform's own encoder ceilings. Fetched for
    // the same reason the guidance is: these are other people's published
    // figures and a second copy in the bundle would drift from the one the
    // server serves.
    api
      .listServices()
      .then((r) => setServicesRead(okRead(r.services)))
      .catch(() => setServicesRead(failedRead()));

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
      setDelaySeconds(destination.delaySeconds ?? 0);
      setLoadedDelay(destination.delaySeconds ?? 0);
      setAudio(destination.audio ?? {});
      setCompliance(destination.compliance ?? {});
      setFacebook(destination.facebook ?? {});
      setBackupIngestWanted(destination.backupIngestWanted ?? false);
      setMultitrack(destination.multitrack ?? false);
      setAccountId(destination.accountId ? String(destination.accountId) : "none");
      // A destination saved before renditions existed has no rendition id at
      // all, which is exactly passthrough — the same thing it has always done.
      setRenditionId(destination.renditionId ? String(destination.renditionId) : PASSTHROUGH);
      // sourceId is NOT set here. The effect that applies initialSourceValue
      // does it, and does it correctly for this case: it reads the
      // destination's own programme. Setting it here as well was belt and
      // braces that no test could distinguish -- removing the line changed no
      // behaviour and no assertion, which is the definition of a line that is
      // not doing anything. Found by mutating it and watching six tests pass.
    } else {
      setTransport({});
      setResilience({});
      setDelaySeconds(0);
      setLoadedDelay(0);
      setAudio({});
      setCompliance({});
      setFacebook({});
      setBackupIngestWanted(false);
      setMultitrack(false);
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
    // reloadAccounts is stable (useCallback with no deps), so listing it does
    // not re-run this effect; it is listed because the effect calls it.
  }, [open, destination, reloadAccounts]);

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
  // Only a platform whose setup guide says it has a device flow can be
  // connected from inside this form without a redirect back to this origin.
  const deviceCodeOK =
    caps?.connect !== undefined &&
    rowsOf(guidesRead).some((g) => g.platform === caps.connect && platformSupportsDeviceCode(g)) &&
    rowsOf(credsRead).some((c) => String(c.platform) === caps.connect);
  // A bare IP origin cannot be a registered redirect target on most platforms.
  // Read from the browser: a self-signed certificate on a name is not visible
  // here, and the dialog does not claim that it is.
  const ipOrigin = typeof window !== "undefined" && isIpLiteralHost(window.location.hostname);
  // A self-signed certificate is invisible from the page, but the server
  // knows what it is serving (A5). Read once per open; unknown reads as no.
  const selfSigned = tlsMode === "selfsigned";
  // Either way the platform's redirect back to this origin is unlikely to
  // work, so the code path leads and says why.
  const codeFirst = ipOrigin || selfSigned;
  // Where the redirect sign-in comes back to, and the server's reasons it may
  // not (A5). Shown before the operator follows the link, because following it
  // leaves this form.
  const connectGuide =
    caps?.connect !== undefined ? rowsOf(guidesRead).find((g) => g.platform === caps.connect) : undefined;
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

  /** Ask on every change of EITHER half. The pair is what is being judged, so a
   *  platform switch with the rendition unchanged has to re-ask — that is the
   *  case the whole warning exists for, an operator moving a working encode to
   *  a destination that will not take it.
   *
   *  A failed request clears rather than keeps: a stale concern shown against a
   *  platform the operator has already moved away from is worse than silence,
   *  because it is confidently about the wrong thing. */
  // Keyed on the ID and the platform, not on the rendition OBJECT. selectedView
  // is a useMemo over a list this dialog refetches, so the object's identity
  // changes on reloads that changed nothing -- which both refetches for no
  // reason and, worse, hides whether `platform` is really a dependency at all.
  // Mutation-tested: with the object in the deps, deleting `platform` from them
  // broke no test, because identity churn was doing the work by accident.
  const concernRenditionId = selectedRendition?.id ?? 0;
  useEffect(() => {
    if (!concernRenditionId) {
      setConcerns([]);
      return;
    }
    let live = true;
    api
      .renditionConcerns(concernRenditionId, platform)
      .then((c) => {
        if (live) setConcerns(c ?? []);
      })
      .catch(() => {
        if (live) setConcerns([]);
      });
    return () => {
      live = false;
    };
  }, [concernRenditionId, platform]);

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
    // Through changeKind, not setKind: picking an SRT or file preset is a
    // transport change like any other, and a key left over from the RTMP one
    // the operator was configuring a moment ago would be stranded exactly the
    // same way. See keyAfterKindChange.
    changeKind(p.kind);
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

  // What Save checks before it asks the server: the two step-1 facts the
  // server would refuse without. Either sends the operator to step 1 with the
  // reason, rather than a disabled button that says nothing about why.
  const saveFromStep = () => {
    if (!name.trim()) {
      setStep(1);
      setStepError(t("addDest.needName"));
      requestAnimationFrame(() => document.getElementById("dest-name")?.focus());
      return;
    }
    if (!sourceIsChosen(sourceId, choices)) {
      setStep(1);
      setStepError(t("addDest.needSource"));
      return;
    }
    setStepError(null);
    void save();
  };

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
        // Which programme this carries. Never undefined: the server refuses a
        // create that names none, and the save below cannot be reached until
        // sourceIsChosen agrees.
        sourceId: sourceIdForSave(sourceId, choices) ?? undefined,
        transport,
        resilience,
        delaySeconds,
        // copyOn rather than audio.copy: switching the transport to RTMP has to
        // drop the flag here, or the operator gets a save error about a control
        // that is no longer on screen to turn off.
        audio: { ...audio, copy: copyOn },
        compliance,
        facebook,
        backupIngestWanted,
        // The bare boolean, for the reason spelled out at the backup toggle
        // below: the PUT is decoded OVER the stored row, so a key
        // JSON.stringify omits is a key the server leaves alone. `false` has
        // to travel or switching this off saves nothing.
        multitrack,
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
        toast.success(t("addDest.toastUpdated"));
      } else {
        ({ warnings = [] } = await api.createDestination(payload));
        toast.success(t("addDest.toastCreated"));
      }
      for (const w of warnings) toast.warning(w, { duration: 10000 });
      onSaved();
      onOpenChange(false);
    } catch (err) {
      // A destination belongs to a programme, so on an install with none this
      // save is refused with code no_source -- and nothing has failed. The
      // dashboard stopped raising a scarlet toast for that state; this dialog
      // is the one control that survives on the page long enough to still be
      // clicked in it, so it must not go on doing so. It closes instead, which
      // lets the empty state underneath be the answer.
      if (isNoSource(err)) {
        toast.info(t("dash.destinationNeedsASource"));
        onOpenChange(false);
        return;
      }
      const message = err instanceof Error ? err.message : t("addDest.saveFailed");
      toast.error(message);
      // To the step the refusal is about (A6), with the sentence beside it.
      const at = stepForError(message);
      if (at) setStep(at);
      setStepError(message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {/* Wider than the old max-w-md: the sections below (platform grid,
          audio, video) need the room, and a narrow column is what made the
          form read as one long scroll. The lead sentence says what the form is
          for before any term of art appears. Terms (routing profile,
          rendition) are explained where they are used, not here. */}
      {/* A full-screen sheet below sm (A6): four steps of fields in a centred
          box on a phone left a strip of page around a form too narrow to read. */}
      <DialogContent className="max-h-[90vh] max-w-2xl overflow-y-auto max-sm:h-[100dvh] max-sm:max-h-[100dvh] max-sm:max-w-none max-sm:rounded-none max-sm:border-0">
        <DialogHeader>
          <DialogTitle>{editing ? t("addDest.titleEdit") : t("addDest.titleAdd")}</DialogTitle>
          <DialogDescription>{t("addDest.description")}</DialogDescription>
        </DialogHeader>

        <DestinationSteps step={step} onStep={setStep} />

        <div className="flex flex-col gap-3">
          <StepPanel n={1} step={step}>
          <div className="flex flex-col gap-1">
            <Label htmlFor="dest-name">{t("dest.name")}</Label>
            <Input
              id="dest-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("dest.namePlaceholder")}
            />
          </div>

          {/* WHICH PROGRAMME THIS CARRIES, and it is second only to the name
              because everything below it is scoped by the answer.

              Shown only when there is a decision to make. With one programme
              this is a control whose every use is the same, and
              DestinationDialog already rules that out for the multitrack switch:
              "offering it elsewhere would be a control that cannot do anything."
              The value is still sent -- initialSourceValue supplies it -- so the
              server's requirement is met without asking for a click that has one
              possible outcome.

              The placeholder is not a default. Nothing is selected until
              somebody selects it, and the save stays disabled meanwhile, which
              is the whole point: the server used to choose the first programme
              when a request named none, and a preselected first item here would
              be the same silent choice wearing a control. */}
          {choices.length > 1 && (
            <div className="flex flex-col gap-1">
              <Label htmlFor="dest-source">{t("dest.source")}</Label>
              <Select value={sourceId} onValueChange={setSourceId}>
                <SelectTrigger id="dest-source" data-testid="source-picker">
                  <SelectValue placeholder={t("dest.sourcePlaceholder")} />
                </SelectTrigger>
                <SelectContent>
                  {choices.map((c) => (
                    <SelectItem key={c.id} value={String(c.id)}>
                      {c.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {/* A disabled control says why, next to itself -- DESIGN-SYSTEM.md.
                  Without this the save button is simply dead and the reason is
                  three fields away. */}
              {!sourceIsChosen(sourceId, choices) && (
                <p className="text-[12px] text-muted-foreground">{t("dest.sourceRequired")}</p>
              )}
            </div>
          )}

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
                {selectedPreset ? selectedPreset.name : t("addDest.choosePlatform")}
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
                    placeholder={t("addDest.searchPlatforms", { count: PRESETS.length })}
                    className="w-full bg-transparent py-2 text-xs outline-none placeholder:text-muted-foreground"
                  />
                </div>
                <div className="max-h-56 overflow-y-auto p-1">
                  {PRESET_GROUPS.map((g) => {
                    const rows = matches.filter((p) => p.group === g.key);
                    if (rows.length === 0) return null;
                    return (
                      <div key={g.key}>
                        <div className="px-2 pt-2 pb-1 text-sm font-medium uppercase tracking-wider text-muted-foreground">
                          {t(g.label)}
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
                                {t("dest.tierNotSupported")}
                              </Badge>
                            )}
                            {/* Partial signs in as much as integrated does —
                                it just cannot fetch the key — so hiding the
                                hint on it would understate what Kick offers.
                                The suffix is what stops "sign in" reading as
                                "and you are done". */}
                            {(rowCaps.tier === "integrated" || rowCaps.tier === "partial") && (
                              // B2 follow-up: at the 12px floor these hints can
                              // starve the platform name on a phone-width dialog,
                              // and in the longer locales overflow the row. They
                              // truncate instead of refusing to shrink; the
                              // transport tag and the name keep their room.
                              <span className="min-w-0 max-w-[40%] truncate text-sm text-muted-foreground">
                                {rowCaps.tier === "partial" ? t("dest.tierSignInPasteKey") : t("dest.tierSignIn")}
                              </span>
                            )}
                            {/* Saying so here saves the operator picking a
                                platform and then wondering why the URL box is
                                still empty. */}
                            {p.kind && !p.url && rowCaps.tier !== "unsupported" && (
                              <span className="min-w-0 max-w-[40%] truncate text-sm text-muted-foreground">
                                {t("dest.urlFromDashboard")}
                              </span>
                            )}
                            <span className="shrink-0 font-mono text-sm uppercase text-muted-foreground">
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
                    <p className="px-2 py-3 text-sm text-muted-foreground">
                      {t("addDest.noMatch", { query })}
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
                <Badge variant={unsupported ? "warn" : "outline"}>{t(tierInfo(caps.tier).label)}</Badge>
                <span className="text-sm text-muted-foreground">{t(tierInfo(caps.tier).help)}</span>
              </div>
              <p className={unsupported ? "text-sm text-warn" : "text-sm text-muted-foreground"}>
                {t(caps.summary)}
              </p>
              {caps.readFirst && (
                <p className="text-sm text-muted-foreground">{t(caps.readFirst)}</p>
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
                      title={t("caps.cellTitle", {
                        column: t(col.label),
                        support: t(info.label),
                        reason: t(caps.reasons?.[col.key] ?? info.help),
                      })}
                    >
                      {t(col.label)}
                      <span className="opacity-70">{t(info.label).toLowerCase()}</span>
                    </Badge>
                  );
                })}
              </div>
              {unsupported && (
                <p className="text-sm text-warn">{t("addDest.unsupportedSaveNote")}</p>
              )}
            </div>
          )}

          </StepPanel>

          <StepPanel n={2} step={step}>
          <div className="flex flex-col gap-1">
            <Label>{t("dest.transport")}</Label>
            <Select value={kind} onValueChange={(v) => changeKind(v as DestKind)}>
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
              {/* #719. A failed listAccounts() used to replace the picker with
                  "Connect {platform} account", which is a re-authorisation for
                  an account that is very likely already linked -- and following
                  it navigates away and discards this half-filled dialog. The
                  connect path is offered only when the server actually
                  answered. */}
              {platformAccounts.length > 0 ? (
                <>
                <Select value={accountId} onValueChange={setAccountId}>
                  <SelectTrigger data-testid="connected-account-picker">
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
                {/* The linked account as a chip (A5): its health from the
                    server's last refresh, a way to unlink it from this
                    destination, and the way to manage it, which stays in
                    Settings. */}
                {(() => {
                  const linked = platformAccounts.find((a) => String(a.id) === accountId);
                  return linked ? <AccountChip account={linked} onUnlink={() => setAccountId("none")} /> : null;
                })()}
                </>
              ) : readFailed(accountsRead) ? (
                <p className="text-sm text-muted-foreground">
                  {t("addDest.accountsReadFailed", { platform: caps.name })}
                </p>
              ) : !mayClaim(accountsRead) ? (
                <p className="text-sm text-muted-foreground">{t("addDest.checkingAccount")}</p>
              ) : (
                <div className="flex flex-col gap-1.5">
                  {/* The code path first where the platform has one: it stays
                      on this page, so the form is kept. The redirect is the
                      fallback, and it leaves the form. */}
                  {deviceCodeOK && (
                    <Button size="sm" onClick={() => setDeviceOpen(true)}>
                      {t("addDest.connectWithCode")}
                    </Button>
                  )}
                  {ipOrigin && <p className="text-sm text-muted-foreground">{t("addDest.ipOriginNote")}</p>}
                  {selfSigned && !ipOrigin && (
                    <p className="text-sm text-muted-foreground">{t("addDest.selfSignedNote")}</p>
                  )}
                  {codeFirst && !deviceCodeOK && (
                    <p className="text-sm text-warn">{t("addDest.noCodePath", { platform: caps.name })}</p>
                  )}
                  <Button variant={deviceCodeOK ? "outline" : "default"} size="sm" asChild>
                    <a href={api.connectUrl(caps.connect)}>
                      <ExternalLink /> {t("addDest.connectAccount", { platform: caps.name })}
                    </a>
                  </Button>
                  {connectGuide?.redirectPath && (
                    <p className="flex flex-wrap items-baseline gap-1 text-sm text-muted-foreground">
                      {t("addDest.redirectReturnsTo")}
                      <code className="break-all font-mono text-xs text-foreground">{connectGuide.redirectPath}</code>
                    </p>
                  )}
                  {connectGuide?.redirectWarnings?.map((w) => (
                    <p key={w} className="text-sm text-warn">
                      {w}
                    </p>
                  ))}
                </div>
              )}
              {/* The mixed case, spelled out. Kick's sign-in is worth having on
                  its own merits — chat, moderation, metadata, viewer count —
                  and an operator who reads "connect an account" as "this is how
                  I get my key" will conclude the feature is broken when the key
                  field stays empty. */}
              <span className="text-sm text-muted-foreground">
                {manualKey
                  ? t("addDest.signInGets", {
                      features: [
                        supportOf(caps, "chatRead") === "yes" && t("addDest.featureChat"),
                        supportOf(caps, "moderation") === "yes" && t("addDest.featureModeration"),
                        supportOf(caps, "metadata") === "yes" && t("addDest.featureMetadata"),
                        supportOf(caps, "viewerStats") === "yes" && t("addDest.featureViewerStats"),
                      ]
                        .filter(Boolean)
                        .join(t("addDest.featureListSeparator")),
                      platform: caps.name,
                    })
                  : t("addDest.linkFetchesKey")}{" "}
                {t("addDest.needsDevCreds")}
              </span>
            </div>
          )}

          {/* The platform's own ingest servers, when it publishes a list. Above
              the URL box rather than replacing it: picking fills the box, and
              the box stays editable, because a regional host we have not heard
              of is the operator's to type and not ours to refuse. */}
          {kind === "rtmp" && (service?.servers?.length ?? 0) > 0 && (
            <div className="flex flex-col gap-1">
              <Label htmlFor="dest-server">{t("addDest.ingestServer", { platform: service?.name ?? "" })}</Label>
              <Select
                value={urlIsAPublishedServer ? url : ""}
                onValueChange={(v) => setUrl(v)}
              >
                <SelectTrigger id="dest-server" data-testid="ingest-server-picker">
                  <SelectValue placeholder={t("addDest.ingestServerPlaceholder")} />
                </SelectTrigger>
                <SelectContent>
                  {service?.servers?.map((sv) => (
                    <SelectItem key={sv.url} value={sv.url}>
                      {sv.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <span className="text-sm text-muted-foreground">{t("addDest.ingestServerHint")}</span>
            </div>
          )}

          {/* Kick, and anything else that issues its ingest per channel: there
              is no server to offer, so the note carries the instructions the
              dashboard leaves out. Shown before the empty box, not after it. */}
          {kind === "rtmp" && service?.perChannelIngest && service.note && (
            <p
              className="rounded-md border border-warn/40 bg-warn/10 p-2 text-sm leading-snug"
              data-testid="per-channel-ingest-note"
            >
              {service.note}
            </p>
          )}

          <div className="flex flex-col gap-1">
            <Label htmlFor="dest-url">
              {kind === "file" ? t("addDest.urlLabelFile") : kind === "srt" ? "SRT URL" : "RTMP URL"}
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
              <span className="text-sm text-muted-foreground">{t("addDest.fileHint")}</span>
            )}
            {/* A preset that could not prefill a URL is not a broken preset; it
                is one whose platform issues the hostname per account. Say that
                where the empty box is, not in a tooltip somewhere else. */}
            {selectedPreset?.kind && !selectedPreset.url && kind !== "file" && (
              <span className="text-sm text-muted-foreground">
                {t("addDest.perAccountUrl", { platform: selectedPreset.name })}
              </span>
            )}
          </div>

          {kind === "rtmp" && (
            <div className="flex flex-col gap-1">
              <Label htmlFor="dest-key">{t("dest.streamKeyLabel")}</Label>
              <SecretInput
                id="dest-key"
                value={streamKey}
                onChange={(e) => setStreamKey(e.target.value)}
                className="font-mono"
                placeholder="xxxx-xxxx-xxxx-xxxx"
              />
              {selectedPreset && !selectedPreset.separateKey && (
                <span className="text-sm text-muted-foreground">
                  {t("addDest.keyInUrl", { platform: selectedPreset.name })}
                </span>
              )}
              {/* Said at the field, where the operator is looking for a Fetch
                  button that is never going to appear. The reason travels with
                  it so this does not read as an apology. */}
              {manualKey && caps && (
                <span className="text-sm text-muted-foreground">
                  {showOAuth
                    ? t("addDest.typeKeyAnyway", { reason: caps.reasons?.streamKey ? t(caps.reasons.streamKey) : "" })
                    : caps.reasons?.streamKey ? t(caps.reasons.streamKey) : t("addDest.noKeyApi", { platform: caps.name })}
                </span>
              )}
            </div>
          )}

          </StepPanel>

          {/* Video and audio sit together on purpose: what a platform receives
              is one answer with two halves, and the pairing is the feature.
              Since A6 they are two steps of the same dialog, Audio before
              Video and advanced, and every value still saves together. */}
          <StepPanel n={4} step={step}>
          {/* Two cards, not a <Select>.
              A dropdown presents copying and encoding as the same kind of
              choice. They are not: copying is `-c:v copy` and costs nothing,
              while an encode is the most expensive thing an operator can switch
              on here. Making that a structural difference — two cards, with
              everything under the second one collapsed until it is chosen — is
              the whole point, and it is what Restreamer already does (its
              filter controls render only when the codec is not `copy`). */}
          <fieldset className="flex flex-col gap-2">
            <legend className="mb-1 flex items-center gap-1.5 text-[12px] font-medium">
              {t("dest.videoTreatment")}
              {/* A6: the first place a rendition is offered, so the first place
                  it is explained. */}
              <InfoHint body="dest.help.rendition" title="dest.videoTreatment" />
            </legend>

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
                  <Badge variant="outline" className="ml-auto">{t("dest.recommended")}</Badge>
                </span>
                <span className="mt-1 block text-sm text-muted-foreground">
                  <span className="font-mono">-c:v copy</span> — {t("dest.copySourceBody")}
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
                // #719. Disabled when there are none AND when we could not ask.
                // The old code could not tell those apart, so a failed read
                // rendered the sentence below as fact.
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
                <span className="mt-1 block text-sm text-muted-foreground">
                  {/* THREE STATES, NOT TWO. #719. "No shared encodes yet"
                      is a POSITIVE CLAIM -- it says the server answered and
                      there are none -- and it sends the operator to the
                      Renditions page to build a second encode they may already
                      have. mayClaim is what makes that sentence unreachable
                      for a read that never landed. */}
                  {readFailed(renditionsRead)
                    ? t("addDest.encodesReadFailed")
                    : !mayClaim(renditionsRead)
                      ? t("addDest.encodesChecking")
                      : renditions.length === 0
                        ? t("addDest.encodesNone")
                        : t("addDest.encodesHelp")}
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
                        <span className="font-mono">{renditionSpec(v.rendition, t)}</span>
                        {v.rendition.name ? ` — ${v.rendition.name}` : ""}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>

                {selectedView && (
                  <span className="text-sm text-muted-foreground">
                    {/* The consequence, computed rather than asserted. Whether
                        this costs a new encode or nothing at all is the fact
                        the whole shared model turns on, and it was previously
                        one static sentence that could not tell the two apart. */}
                    {joinConsequence(selectedView)}
                  </span>
                )}
                {selectedRendition?.note && (
                  <span className="text-sm text-muted-foreground">{selectedRendition.note}</span>
                )}

                {/* #661. The catalogue knew this all along and nothing read it
                    at the moment a figure is chosen; the only caller logged at
                    stream start, after the operator had committed.

                    Not a block on saving, deliberately. The catalogue is a
                    snapshot of someone else's documentation and can be the stale
                    half — X's own two pages disagree materially — so the source
                    and the date it was checked are shown WITH the objection, and
                    an operator who knows better can proceed. */}
                {concerns.length > 0 && (
                  <div
                    data-testid="rendition-concerns"
                    className="flex flex-col gap-1 rounded-md border border-warn/40 bg-warn/10 px-2 py-1.5"
                  >
                    <span className="text-sm font-medium text-warn">
                      {t("dest.renditionConcerns")}
                    </span>
                    {concerns.map((c) => (
                      <span key={c.field} className="text-sm text-muted-foreground">
                        {c.detail}{" "}
                        <a
                          href={c.source}
                          target="_blank"
                          rel="noreferrer noopener"
                          className="underline underline-offset-2"
                        >
                          {c.checked}
                        </a>
                      </span>
                    ))}
                  </div>
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
                    <summary className="cursor-pointer text-sm font-medium">
                      {t("dest.customiseSummary")}
                    </summary>
                    <p className="mt-1.5 text-sm text-muted-foreground">
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
                        className="mt-2 text-left text-sm text-primary underline hover:opacity-80"
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
                      <p className="mt-1.5 text-sm text-down">{variantErr}</p>
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
                          setVariantErr(t("addDest.variantNeedsBitrate"));
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
                          setRenditionsRead(okRead(rows));
                          setRenditionId(String(made.rendition.id));
                          setVariantOpen(false);
                        } catch (e) {
                          setVariantErr(e instanceof Error ? e.message : t("addDest.variantCreateFailed"));
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
                  <span className="text-sm font-medium">
                    {t("addDest.platformPublishes", {
                      platform: guidance.find((g) => g.id === presetId)?.name ?? t("addDest.thisPlatform"),
                    })}
                  </span>
                  <span className="font-mono text-sm text-muted-foreground">
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
                      platformVideo.gopSeconds
                        ? t("addDest.gopKeyframes", { seconds: platformVideo.gopSeconds })
                        : null,
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                  </span>
                </span>
                {platformVideo.note && (
                  <p className="mt-1 text-sm leading-relaxed text-muted-foreground">
                    {platformVideo.note}
                  </p>
                )}
                {/* The disclaimer this catalogue has always carried, applied to
                    numbers rather than to hostnames. A figure whose provenance
                    is not on screen is indistinguishable from a guess. */}
                <p className="mt-1 text-sm text-muted-foreground">
                  {tn("addDest.figuresProvenance", {
                    link: (
                      <a
                        href={platformVideo.source}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="underline hover:text-foreground"
                      >
                        {t("addDest.publishedByPlatform")}
                      </a>
                    ),
                    checked: platformVideo.checked,
                  })}
                </p>
              </div>
            )}

            {leaving && (
              <span className="text-sm text-warn">
                {leaveConsequence(leaving)}
              </span>
            )}
          </fieldset>
          </StepPanel>

          <StepPanel n={3} step={step}>
          {/* Which ingest tracks reach this destination is the routing profile,
              set on the Routing page; this step is the audio encode. */}
          <p className="flex flex-wrap items-center gap-1 text-sm text-muted-foreground">
            <span>{t("addDest.routingNote")}</span>
            <InfoHint body="addDest.routingHelp" title="addDest.routingTitle" />
            {editing && (
              <Link to={`/routing/${destination.id}`} className="text-primary-bright underline-offset-4 hover:underline">
                {t("addDest.openRouting")}
              </Link>
            )}
          </p>
          {canCopyAudio && (
            <div className="flex flex-col gap-1">
              <Label htmlFor="dest-audio-copy">{t("addDest.stepAudio")}</Label>
              <div className="flex h-9 items-center gap-2">
                <Switch
                  id="dest-audio-copy"
                  checked={copyOn}
                  onCheckedChange={(v) => setAudio({ ...audio, copy: v })}
                />
                <span className="text-sm text-muted-foreground">
                  {copyOn ? t("addDest.audioCopyOn") : t("addDest.audioCopyOff")}
                </span>
              </div>
              <span className="text-sm text-muted-foreground">
                {copyOn ? t("addDest.audioCopyOnHelp") : t("addDest.audioCopyOffHelp")}
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
              <span className="text-sm text-muted-foreground">{t("addDest.kbpsAacStereo")}</span>
            </div>
            <span className="text-sm text-muted-foreground">
              {copyOn ? t("addDest.bitrateUnusedCopy") : t("addDest.bitrateMixHelp")}
            </span>
            {/* The platform's own ceiling, read from the registry rather than
                recomputed here. Over it is not refused — the server decides
                that, and a second copy of the rule in this file is how the two
                come to disagree. */}
            {!copyOn && (service?.recommended.maxAudioKbps ?? 0) > 0 && (
              <span
                className={
                  bitrate > (service?.recommended.maxAudioKbps ?? 0)
                    ? "text-sm text-warn"
                    : "text-sm text-muted-foreground"
                }
                data-testid="audio-ceiling-hint"
              >
                {bitrate > (service?.recommended.maxAudioKbps ?? 0)
                  ? t("addDest.audioAboveMax", {
                      platform: service?.name ?? "",
                      max: service?.recommended.maxAudioKbps ?? 0,
                    })
                  : t("addDest.audioAcceptsUpTo", {
                      platform: service?.name ?? "",
                      max: service?.recommended.maxAudioKbps ?? 0,
                    })}
              </span>
            )}
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
                  <SelectItem value="aac">{t("addDest.codecAac")}</SelectItem>
                  <SelectItem value="opus">{t("addDest.codecOpus")}</SelectItem>
                </SelectContent>
              </Select>
              <span className="text-sm text-muted-foreground">
                {copyOn
                  ? t("addDest.codecUnusedCopy")
                  : kind === "rtmp"
                    ? t("addDest.codecRtmpAac")
                    : t("addDest.codecOpusHint")}
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
                <span className="text-sm text-muted-foreground">
                  {audio.mono ? t("addDest.mono") : t("addDest.stereo")}
                </span>
              </div>
              <span className="text-sm text-muted-foreground">
                {copyOn ? t("addDest.monoUnusedCopy") : t("addDest.monoHelp")}
              </span>
            </div>
          </div>

          </StepPanel>

          <StepPanel n={4} step={step}>
          {(platform === "youtube" || platform === "twitch" || platform === "facebook") && (
            <div className="flex flex-col gap-3 rounded-md border border-warn/40 bg-warn/5 p-2">
              <p className="text-xs font-medium">{t("dest.compliance")}</p>
              <span className="text-sm text-muted-foreground">{t("addDest.complianceIntro")}</span>

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
                    <span className="text-sm text-muted-foreground">{t("addDest.visibilityHelp")}</span>
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
                        <SelectItem value="no">{t("addDest.kidsNo")}</SelectItem>
                        <SelectItem value="yes">{t("addDest.kidsYes")}</SelectItem>
                      </SelectContent>
                    </Select>
                    <span className="text-sm text-muted-foreground">{t("addDest.kidsHelp")}</span>
                  </div>
                </>
              )}

              {platform === "twitch" && (
                <div className="flex flex-col gap-1">
                  <Label>{t("dest.contentLabels")}</Label>
                  <div className="flex flex-col gap-1">
                    {TWITCH_LABELS.map((id) => (
                      <div key={id} className="flex items-center justify-between gap-2">
                        <span className="text-sm">{t(TWITCH_LABEL_KEYS[id])}</span>
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
                  <span className="text-sm text-muted-foreground">{t("addDest.twitchLabelsHelp")}</span>
                </div>
              )}

              {platform === "facebook" && facebookTargetsAPage && (
                <span className="text-sm text-muted-foreground">{t("addDest.fbPageAudience")}</span>
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
                    {/* By test id, the way the rendition picker is, and for the
                        same reason video-treatment.spec.ts records: the dialog
                        carries a dozen comboboxes and the Label above this one
                        is not associated with it, so a role-based locator picks
                        whichever one happens to come first and the test dies on
                        a timeout instead of an assertion. */}
                    <SelectTrigger data-testid="fb-audience-picker">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="unset">{t("dest.leaveOnFacebook")}</SelectItem>
                      {/* Rendered FROM lib/facebookPrivacy, not typed out here.
                          That array is pinned to db.FacebookPrivacies by
                          internal/db/compliance_drift_test.go, and the pin only
                          means something while this select is the array's one
                          consumer: an option added to the enum and missed here
                          would otherwise be a Facebook audience no operator can
                          choose, with every line of handling behind it dead. */}
                      {FACEBOOK_PRIVACIES.map((p) => (
                        // data-value because Radix puts the option's VALUE
                        // nowhere in the DOM -- only its label. ui/e2e reads
                        // the audiences this select is offering off the page
                        // and round-trips each one through the server, and
                        // reading labels instead would make that a test of the
                        // English catalogue rather than of the enum.
                        <SelectItem key={p.value} value={p.value} data-value={p.value}>
                          {t(p.labelKey)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <span className="text-sm text-muted-foreground">{t("addDest.fbAudienceHelp")}</span>
                </div>
              )}
            </div>
          )}

          {/* Gated the same way the Facebook box below is: by the selected
              platform, not by whether an account is connected. Twitch is the
              one platform that publishes this negotiation, and offering it
              elsewhere would be a control that cannot do anything. The label is
              the feature's name, which every catalogue keeps in English. */}
          {platform === "twitch" && (
            <div className="flex flex-col gap-1">
              <Label htmlFor="dest-multitrack" className="flex items-center gap-1.5">
                Enhanced Broadcasting (Twitch)
                <ExperimentalBadge />
              </Label>
              <div className="flex h-9 items-center gap-2">
                <Switch
                  id="dest-multitrack"
                  checked={multitrack}
                  // The bare setter, so `false` is a value this dialog can
                  // actually send. See the payload note at `multitrack` above.
                  onCheckedChange={setMultitrack}
                />
                <span className="text-sm text-muted-foreground">
                  {multitrack ? t("addDest.multitrackOn") : t("addDest.multitrackOff")}
                </span>
              </div>
              {/* Not a warning, and deliberately not styled as one. A
                  negotiation that does not succeed is the EXPECTED outcome on
                  the machine most operators run this on, so saying it in amber
                  would train them to read a normal broadcast as broken. */}
              <span className="text-sm text-muted-foreground">{t("addDest.multitrackHelp")}</span>
              {/* The two things the sentence above leaves out, and both are
                  things an operator would otherwise have to guess: nothing is
                  asked at all until the machine's GPU is declared, and the
                  answer is a line on this destination's card rather than a
                  dialog nobody is standing in front of at go-live. */}
              <span className="text-sm text-muted-foreground">{t("addDest.multitrackGpuNote")}</span>
              {/* The switch above still works and is still one click. What is
                  added here is the one fact the paragraphs above cannot supply,
                  because it is about our evidence rather than about Twitch:
                  where the measurements stop. They stop after Negotiate
                  returns, not before it. */}
              <Experimental>
                {tn("addDest.multitrackEvidence", { em: <em>{t("addDest.multitrackEvidenceEm")}</em> })}
              </Experimental>
            </div>
          )}

          {/* Not compliance: neither field is an obligation, and both are
              create-time-only, same as Audience above -- so this is its own
              box rather than folded into the amber one. See db.FacebookSettings. */}
          {platform === "facebook" && (
            <div className="flex flex-col gap-3 rounded-md border border-border p-2">
              <p className="text-xs font-medium">{t("dest.fbBoxTitle")}</p>
              <span className="text-sm text-muted-foreground">{t("dest.fbBoxIntro")}</span>

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
                  <span className="text-sm text-muted-foreground">
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
                <span className="text-sm text-muted-foreground">
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
                <span className="text-sm text-muted-foreground">
                  {t("dest.fbDonateHelp")}
                </span>
              </div>
            </div>
          )}

          <details className="rounded-md border border-border p-2">
            <summary className="cursor-pointer text-xs font-medium">
              {t("addDest.transportMuxer")}
              {(transport.noDurationFilesize ||
                (transport.muxQueuePackets ?? 0) > 0 ||
                (transport.rwTimeoutSeconds ?? 0) > 0) && (
                <span className="ml-2 text-sm text-muted-foreground">{t("addDest.customised")}</span>
              )}
            </summary>
            <div className="mt-2 flex flex-col gap-3">
              <span className="text-sm text-muted-foreground">{t("addDest.transportIntro")}</span>

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
                <span className="text-sm text-muted-foreground">{t("addDest.socketTimeoutHelp")}</span>
              </div>

              {kind === "rtmp" && (
                <div className="flex items-center justify-between gap-2">
                  <Label htmlFor="dest-flv" className="font-normal">
                    {t("addDest.flvDropLabel")}
                  </Label>
                  <Switch
                    id="dest-flv"
                    checked={transport.noDurationFilesize ?? false}
                    onCheckedChange={(v) => setTransport({ ...transport, noDurationFilesize: v })}
                  />
                </div>
              )}
              {kind === "rtmp" && (
                <span className="text-sm text-muted-foreground">{t("addDest.flvDropHelp")}</span>
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
                  <Label htmlFor="dest-mqb">{t("addDest.muxQueueBytes")}</Label>
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
              <span className="text-sm text-muted-foreground">
                {t("dest.muxQueuePair")}
              </span>

              <div className="flex flex-col gap-1 border-t border-border pt-3">
                <Label htmlFor="dest-delay">{t("delay.field")}</Label>
                <Input
                  id="dest-delay"
                  type="number"
                  min={0}
                  max={900}
                  step={1}
                  value={delaySeconds}
                  onChange={(e) =>
                    setDelaySeconds(Math.max(0, Math.min(900, Math.round(Number(e.target.value) || 0))))
                  }
                  className="w-24"
                />
                <span className="text-sm text-muted-foreground">{t("delay.help")}</span>
                {/* Said BEFORE the save: a delay cannot be stretched on a
                    running buffer, so the destination restarts and refills. */}
                {destination?.enabled && delaySeconds !== loadedDelay && (
                  <span data-testid="delay-restart-warning" className="text-sm text-warn">
                    {t("delay.liveRestart", { seconds: delaySeconds })}
                  </span>
                )}
              </div>

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
                  <Label htmlFor="dest-maxbo">{t("addDest.backoffMax")}</Label>
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
              <span className="text-sm text-muted-foreground">{t("addDest.backoffHelp")}</span>

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
                <span className="text-sm text-muted-foreground">{t("addDest.giveUpHelp")}</span>
              </div>
            </div>
          </details>

          </StepPanel>

          <StepPanel n={2} step={step}>
          {/* The platform's long setup prose sits behind a disclosure, so the
              form shows the fields first. Facebook's notes alone run to about a
              thousand characters; read on every open, they buried the key field. */}
          {selectedPreset && (
            <details className="rounded-md border border-dashed p-2">
              <summary className="cursor-pointer text-sm font-medium">
                {t("addDest.howToGetKey")}
              </summary>
              <div className="flex flex-col gap-1 pt-2">
                <p className="text-sm text-muted-foreground">{t(selectedPreset.notes)}</p>
                <p className="text-sm text-muted-foreground">{t(PRESET_DISCLAIMER)}</p>
                {selectedPreset.helpUrl && (
                  <a
                    href={selectedPreset.helpUrl}
                    target="_blank"
                    rel="noreferrer noopener"
                    className="inline-flex items-center gap-1 text-sm underline underline-offset-2"
                  >
                    <ExternalLink className="size-3" /> {t("addDest.setupDocs", { platform: selectedPreset.name })}
                  </a>
                )}
              </div>
            </details>
          )}
          </StepPanel>
        </div>

        {stepError && (
          <p role="alert" className="text-sm text-down">
            {stepError}
          </p>
        )}

        <DialogFooter className="gap-2 sm:justify-between">
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <div className="flex flex-wrap items-center justify-end gap-2">
            {step > 1 && (
              <Button variant="outline" onClick={() => setStep((s) => (s - 1) as DestStep)}>
                {t("addDest.back")}
              </Button>
            )}
            {step < 4 && (
              <Button variant={editing ? "outline" : "default"} onClick={() => setStep((s) => (s + 1) as DestStep)}>
                {t("addDest.next")}
              </Button>
            )}
            {/* Save on every step when editing: the operator who came to change
                a key should not walk past three steps to keep it. A new
                destination saves from the last step, after it has been seen. */}
            {(editing || step === 4) && (
              <Button onClick={saveFromStep} disabled={busy}>
                {busy && <Loader2 className="animate-spin" />}
                {editing ? t("common.save") : t("hooks.create")}
              </Button>
            )}
          </div>
        </DialogFooter>
        {deviceCodeOK && caps?.connect && (
          <DeviceCodeDialog
            platform={caps.connect}
            platformName={caps.name}
            open={deviceOpen}
            onOpenChange={setDeviceOpen}
            onConnected={reloadAccounts}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}

/** The four-step header (A6). Every step is reachable at any time, adding or
 *  editing: the steps order the form, they do not gate it. */
function DestinationSteps({ step, onStep }: Readonly<{ step: DestStep; onStep: (s: DestStep) => void }>) {
  const t = useT();
  const label: Record<DestStep, TranslationKey> = {
    1: "addDest.stepPlatform",
    2: "addDest.stepConnect",
    3: "addDest.stepAudio",
    4: "addDest.stepVideo",
  };
  return (
    <ol className="grid grid-cols-2 gap-1.5 sm:grid-cols-4" aria-label={t("addDest.stepsLabel")}>
      {DEST_STEPS.map((n) => (
        <li key={n}>
          <button
            type="button"
            onClick={() => onStep(n)}
            aria-current={n === step ? "step" : undefined}
            data-testid={`dest-step-${n}`}
            className={cn(
              "flex w-full items-center gap-2 rounded-md border px-2 py-1.5 text-left text-sm transition-colors",
              n === step
                ? "border-primary bg-primary-dim text-foreground"
                : "border-border text-muted-foreground hover:bg-card-raised hover:text-foreground",
            )}
          >
            <span
              className={cn(
                "flex h-5 w-5 shrink-0 items-center justify-center rounded-full border font-mono text-xs",
                n === step ? "border-primary-bright text-primary-bright" : "border-border-strong",
              )}
              aria-hidden="true"
            >
              {n}
            </span>
            <span className="truncate">{t(label[n])}</span>
          </button>
        </li>
      ))}
    </ol>
  );
}

/** One step's fields. Hidden rather than unmounted, so what was typed on a
 *  step survives leaving it. */
function StepPanel({ n, step, children }: Readonly<{ n: DestStep; step: DestStep; children: ReactNode }>) {
  return (
    <div hidden={n !== step} className="flex flex-col gap-3" data-step={n}>
      {children}
    </div>
  );
}

/** Twitch's content classification labels, by the id Twitch's API takes. */
const TWITCH_LABEL_KEYS: Record<(typeof TWITCH_LABELS)[number], TranslationKey> = {
  DebatedSocialIssuesAndPolitics: "addDest.twitchLabel.DebatedSocialIssuesAndPolitics",
  DrugsIntoxication: "addDest.twitchLabel.DrugsIntoxication",
  Gambling: "addDest.twitchLabel.Gambling",
  ProfanityVulgarity: "addDest.twitchLabel.ProfanityVulgarity",
  SexualThemes: "addDest.twitchLabel.SexualThemes",
  ViolentGraphic: "addDest.twitchLabel.ViolentGraphic",
};

const HEALTH_TONE: Record<NonNullable<PlatformAccount["health"]>["state"], "live" | "warn" | "down"> = {
  ok: "live",
  expiring: "warn",
  refreshFailing: "down",
  expired: "down",
};

const HEALTH_LABEL: Record<NonNullable<PlatformAccount["health"]>["state"], TranslationKey> = {
  ok: "addDest.healthOk",
  expiring: "addDest.healthExpiring",
  refreshFailing: "addDest.healthFailing",
  expired: "addDest.healthExpired",
};

/** The account a destination is linked to, and how it is doing (A5). */
export function AccountChip({ account, onUnlink }: Readonly<{ account: PlatformAccount; onUnlink: () => void }>) {
  const t = useT();
  const health = account.health;
  const expires = account.expiresAt && !account.expiresAt.startsWith("0001") ? new Date(account.expiresAt) : null;
  return (
    <div
      className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-card-raised px-2 py-1.5"
      data-testid="account-chip"
    >
      {health && <StatusDot tone={HEALTH_TONE[health.state]} size="sm" />}
      <span className="text-sm font-medium">{account.accountName}</span>
      {health && (
        <span className={cn("text-sm", health.state === "ok" ? "text-muted-foreground" : HEALTH_TONE[health.state] === "warn" ? "text-warn" : "text-down")}>
          {t(HEALTH_LABEL[health.state])}
        </span>
      )}
      {health?.state !== "ok" && expires && (
        <span className="tnum font-mono text-sm text-muted-foreground">
          {t("addDest.expiresAt", { date: expires.toLocaleString() })}
        </span>
      )}
      {health?.state === "refreshFailing" && health.lastError && (
        <span className="basis-full text-sm text-muted-foreground">{health.lastError}</span>
      )}
      <span className="ml-auto flex items-center gap-1">
        <Button type="button" variant="ghost" size="sm" onClick={onUnlink}>
          {t("addDest.unlink")}
        </Button>
        <Button type="button" variant="ghost" size="sm" asChild>
          <Link to="/settings?tab=platforms">{t("addDest.manageAccounts")}</Link>
        </Button>
      </span>
    </div>
  );
}

