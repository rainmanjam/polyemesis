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
import type {
  Destination,
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
      "Kick does not expose stream keys over its public API, so there is nothing to connect — copy both the ingest URL and the key from Kick → Settings → Stream. Kick issues the ingest host per channel, so replace the one prefilled here with yours if it differs.",
  },
  {
    id: "facebook",
    name: "Facebook Live",
    group: "major",
    transport: "rtmps",
    kind: "rtmp",
    url: "rtmps://live-api-s.facebook.com:443/rtmp/",
    separateKey: true,
    helpUrl: "https://www.facebook.com/live/producer",
    notes:
      "Create the broadcast in Live Producer and copy the stream key; a persistent key lets you reuse this destination across broadcasts. Facebook requires RTMPS — plain RTMP is refused.",
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
    helpUrl: "https://www.facebook.com/live/producer",
    notes:
      "Instagram issues the server URL and key per broadcast, and only to accounts with Live Producer access. Start the broadcast in Meta's Live Producer, then copy the server URL and key from there — there is no fixed Instagram ingest host to prefill.",
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
      "X issues an ingest URL and key per source in Media Studio → Producer. Create the source there and copy both fields.",
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
    notes: "Copy the server URL and stream key from DLive → Dashboard → Stream settings.",
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
      "Rumble Studio issues an ingest URL and key per stream. Set the stream up in Rumble Studio and copy both fields from its RTMP details.",
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

/** Which platforms can hand us a stream key over OAuth. Kick is deliberately
 *  absent: its public API does not expose stream keys — see internal/oauth. */
const OAUTH_PLATFORMS: Platform[] = ["youtube", "twitch"];

/** The label for the connect affordance. Kept separate from preset names
 *  because a connected account belongs to the platform, not to one of the two
 *  ingests that platform offers. */
const PLATFORM_LABELS: Record<Platform, string> = {
  custom: "Custom",
  youtube: "YouTube",
  twitch: "Twitch",
  kick: "Kick",
};

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
      setAccountId(destination.accountId ? String(destination.accountId) : "none");
      // A destination saved before renditions existed has no rendition id at
      // all, which is exactly passthrough — the same thing it has always done.
      setRenditionId(destination.renditionId ? String(destination.renditionId) : PASSTHROUGH);
    } else {
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
  const platformAccounts = useMemo(
    () => accounts.filter((a) => a.platform === platform),
    [accounts, platform],
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

  const showOAuth = OAUTH_PLATFORMS.includes(platform);

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
                        {rows.map((p) => (
                          <button
                            key={p.id}
                            type="button"
                            onClick={() => applyPreset(p)}
                            className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-xs hover:bg-accent"
                          >
                            <span className="flex-1 truncate">{p.name}</span>
                            {/* Saying so here saves the operator picking a
                                platform and then wondering why the URL box is
                                still empty. */}
                            {p.kind && !p.url && (
                              <span className="shrink-0 text-[9px] text-muted-foreground">
                                URL from dashboard
                              </span>
                            )}
                            <span className="shrink-0 font-mono text-[9px] uppercase text-muted-foreground">
                              {p.transport}
                            </span>
                            {p.id === presetId && <Check className="size-3 shrink-0" />}
                          </button>
                        ))}
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

          {showOAuth && (
            <div className="flex flex-col gap-1">
              <Label>Connected account</Label>
              {platformAccounts.length > 0 ? (
                <Select value={accountId} onValueChange={setAccountId}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">Not linked (enter key manually)</SelectItem>
                    {platformAccounts.map((a) => (
                      <SelectItem key={a.id} value={String(a.id)}>
                        {a.accountName}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              ) : (
                <Button variant="outline" size="sm" asChild>
                  <a href={api.connectUrl(platform)}>
                    <ExternalLink /> Connect {PLATFORM_LABELS[platform]} account
                  </a>
                </Button>
              )}
              <span className="text-[10px] text-muted-foreground">
                Linking an account lets polyemesis fetch the stream key for you. Requires
                developer credentials in Settings → Platform credentials.
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
