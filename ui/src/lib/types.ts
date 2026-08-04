/** Types mirroring the Go API. Kept hand-written and small rather than
 *  generated, so the shapes the UI actually consumes stay obvious. */

export const MAX_TRACKS = 6;

export type RoutingMode = "simple" | "matrix";
export type NormalizeMode = "auto" | "off" | "limiter" | "loudnorm";

export interface TrackSel {
  track: number;
  enabled: boolean;
  gain: number;
}

export interface MatrixCell {
  track: number;
  channel: number;
  /** 0 = left, 1 = right */
  out: number;
  gain: number;
}

// ------------------------------------------------------------- track roles
//
// A role is what the operator says a track *is* — "the licensed music", "the
// Spanish commentary" — as opposed to "track 3". Roles live on the SOURCE,
// not on a destination, because the answer does not change per platform.
//
// Roles are inert by themselves: nothing is filtered unless a destination asks
// for it through `excludeRoles`. That asymmetry is deliberate and the UI has
// to preserve it — labelling a track must never silently drop audio.

/** "" is the zero value: a track nobody has described yet. */
export type TrackRole =
  | ""
  | "music"
  | "mic"
  | "game"
  | "commentary"
  | "clean"
  | "other";

/** The catalogue, in the order routing.TrackRoles() offers it. "" is absent:
 *  "no role" is the absence of an annotation, not a choice. */
export const TRACK_ROLES = [
  "mic",
  "commentary",
  "game",
  "music",
  "clean",
  "other",
] as const satisfies readonly Exclude<TrackRole, "">[];

export const ROLE_LABEL: Record<Exclude<TrackRole, "">, string> = {
  mic: "Mic",
  commentary: "Commentary",
  game: "Game",
  music: "Music",
  clean: "Clean",
  other: "Other",
};

/** Matches routing.MaxLabelLen / MaxLangTagLen. */
export const MAX_LABEL_LEN = 64;
export const MAX_LANG_TAG_LEN = 35;

/** The operator's description of one ingest track. Keyed by index rather than
 *  by position, so it survives a track vanishing and coming back. */
export interface TrackAnnotation {
  track: number;
  role?: TrackRole;
  label?: string;
  language?: string;
  denoise?: boolean;
}

// -------------------------------------------------------- destination audio

/** Programme-loudness target. Absent leaves the fixed loudnorm parameters that
 *  shipped with `loudnorm` alone, byte for byte. */
export interface Loudness {
  targetLufs: number;
  /** 0 means routing.DefaultTruePeakDB (−1 dBTP). */
  truePeakDb?: number;
  /** 0 means routing.DefaultLoudnessLRA (11 LU). */
  rangeLu?: number;
}

export const LUFS_STREAMING = -14;
export const LUFS_PODCAST = -16;
export const LUFS_BROADCAST = -23;
export const DEFAULT_TRUE_PEAK_DB = -1;
export const DEFAULT_LOUDNESS_LRA = 11;

export const MIN_TARGET_LUFS = -70;
export const MAX_TARGET_LUFS = -5;
export const MIN_TRUE_PEAK_DB = -9;
export const MAX_TRUE_PEAK_DB = 0;
export const MIN_LOUDNESS_LRA = 1;
export const MAX_LOUDNESS_LRA = 50;

/** Audio against video, in ms. Positive holds AUDIO back (lip-sync repair, a
 *  moderation delay); negative pulls it ahead, which the compiler pays for by
 *  holding the VIDEO back instead. The bounds are asymmetric for that reason. */
export const MIN_DELAY_MS = -2000;
export const MAX_DELAY_MS = 30000;

/** Pull one group of tracks down whenever another is speaking. `trigger` is
 *  what causes the duck (the mic), `target` is what gets pushed down (the
 *  music). They must be disjoint. */
export interface Ducking {
  trigger: number[];
  target: number[];
  /** Each 0 means the routing default below. */
  thresholdDb?: number;
  ratio?: number;
  attackMs?: number;
  releaseMs?: number;
}

export const DEFAULT_DUCK_THRESHOLD_DB = -24;
export const DEFAULT_DUCK_RATIO = 8;
export const DEFAULT_DUCK_ATTACK_MS = 20;
export const DEFAULT_DUCK_RELEASE_MS = 300;

export const MIN_DUCK_THRESHOLD_DB = -60;
export const MAX_DUCK_THRESHOLD_DB = 0;
export const MIN_DUCK_RATIO = 1;
export const MAX_DUCK_RATIO = 20;

export interface RoutingProfile {
  mode: RoutingMode;
  tracks: TrackSel[];
  matrix: MatrixCell[] | null;
  normalize: NormalizeMode;
  sampleRate: number;

  /** Every field below is optional and absent means "behaves exactly as
   *  before". Send them omitted, never as zero values, or a saved profile
   *  stops compiling to the string it used to. */
  loudness?: Loudness | null;
  delayMs?: number;
  ducking?: Ducking | null;
  excludeRoles?: TrackRole[] | null;
}

export interface RoutingResult {
  filterComplex: string;
  outLabel: string;
  summary: string;
  tracks: number[];
  normalization: NormalizeMode;
  warnings: string[] | null;
  /** How long the VIDEO is held back to satisfy a negative `delayMs`. Zero for
   *  every other profile. */
  videoDelayMs?: number;
}

export interface SourceTrack {
  index: number;
  channels: number;
  codec: string;
  layout: string;
  language?: string;
  title?: string;
}

export interface VideoStream {
  codec: string;
  width: number;
  height: number;
  frameRate: number;
  bitrate: number;
  pixFmt: string;
}

export interface SourceInfo {
  probed: boolean;
  tracks: SourceTrack[] | null;
  video?: VideoStream | null;
  /** `tracks` describes the silence tier's synthetic output rather than the
   *  ingest's, because the ingest carries no audio at all. */
  synthetic?: boolean;
  /** What the operator has said these tracks are. Absent on a server that does
   *  not persist annotations yet. */
  annotations?: TrackAnnotation[] | null;
}

export type DestKind = "rtmp" | "srt" | "file";
export type Platform = "custom" | "youtube" | "twitch" | "kick" | "facebook";

export interface Destination {
  id: number;
  name: string;
  /** Optional muxer and socket tuning. Always present in a server response and
   *  empty for a destination that has not opted in. */
  transport?: DestTransport;
  /** Reconnect policy. Always present in a server response and empty for a
   *  destination that has not opted in. */
  resilience?: DestResilience;
  /** Output audio encoding. Always present in a server response and empty for
   *  a destination that has not opted in. */
  audio?: AudioEncoding;
  /** Compliance metadata. Always present in a server response and empty for a
   *  destination that has not set any. */
  compliance?: Compliance;
  /** Facebook create-time configuration: which Pages to crosspost to and which
   *  charity's donate button to attach. Always present in a server response
   *  and empty for a destination that has not set any. */
  facebook?: FacebookSettings;
  kind: DestKind;
  platform: Platform;
  accountId?: number | null;
  url: string;
  streamKey: string;
  /** The platform's secondary ingest, stored when the broadcast was created.
   *  Empty when the platform offered none. */
  backupUrl?: string;
  backupStreamKey?: string;
  enabled: boolean;
  audioBitrate: number;
  profile: RoutingProfile;
  position: number;
  /** The shared video encode this destination reads. null or absent means
   *  passthrough: no encode, straight off the ingest relay. */
  renditionId?: number | null;
  createdAt: string;
  updatedAt: string;
}

/** One shared video encode.
 *
 *  A rendition re-encodes VIDEO ONLY — every audio track is copied through it
 *  untouched, and each destination still applies its own routing graph on top.
 *  That is why there is no audio field here and must never be one. */
export interface Rendition {
  id: number;
  name: string;
  /** 0 on an axis means "keep the source's", so setting only height rescales
   *  while preserving aspect. */
  width: number;
  height: number;
  /** 0 keeps the source's frame rate. */
  fps: number;
  /** Target video bitrate in kbps. */
  videoBitrate: number;
  /** An FFmpeg encoder name. Not a union: which encoders exist is a property
   *  of the running FFmpeg build, and GET /encoders is the only honest source
   *  for that. */
  encoder: string;
  /** The encoder's own speed/quality knob — "veryfast" for x264, "p4" for
   *  nvenc. The vocabulary is per-encoder, hence free text. */
  preset: string;
  /** Keyframe interval in seconds rather than frames, so it survives an fps
   *  change. */
  gopSeconds: number;
  /** How a frame whose shape does not match width x height is reconciled:
   *  "" stretches (the historical behaviour), "crop" centre-crops, "pad"
   *  letterboxes with padColor, "blurpad" fills with a blurred copy of the
   *  frame. Only meaningful when BOTH width and height are set — with one axis
   *  free the scale already preserves aspect and the server refuses the pair. */
  aspectMode?: RenditionAspectMode;
  /** The "pad" fill colour; empty means black. Ignored by the other modes.
   *  A single token, because it lands on a filter graph where a comma would
   *  end the argument. */
  padColor?: string;
  /** Strips field combing before any scaling: "" off, "auto" only frames the
   *  source flagged interlaced, "all" unconditionally. */
  deinterlace?: RenditionDeinterlace;
  /** An optional image watermark burned into this tier. An overlay is the one
   *  thing that makes a rendition re-encode differently from before, so it is
   *  always present in the payload and empty when there is none. */
  overlay: RenditionOverlay;
  /** An optional line of text burned into this tier, drawn ON TOP of the
   *  overlay. Always present in the payload and empty when there is none, for
   *  the same reason `overlay` is. */
  text: RenditionText;
  note: string;
  createdAt: string;
  updatedAt: string;
}
/** Mirrors ffmpeg.AspectModes. The empty string is the zero value and the
 *  historical behaviour, so it must stay in the union. */
export type RenditionAspectMode = "" | "crop" | "pad" | "blurpad";
/** Mirrors ffmpeg.DeinterlaceModes. */
export type RenditionDeinterlace = "" | "auto" | "all";

/** Where an overlay is pinned. Nine positions, named the way an operator would
 *  point at them rather than as coordinates. */
export type OverlayAnchor =
  | "top-left" | "top-center" | "top-right"
  | "middle-left" | "center" | "middle-right"
  | "bottom-left" | "bottom-center" | "bottom-right";

/** An image watermark on a rendition.
 *
 *  Every measurement is a FRACTION of the output (0-1), never pixels. That is
 *  the point rather than a detail: the same overlay has to be correct on a
 *  1920x1080 tier and a 1080x1920 one, and pixel geometry that looks right on
 *  the first lands off-canvas on the second. */
export interface RenditionOverlay {
  /** A path relative to the data directory. Empty means no overlay. */
  image?: string;
  anchor?: OverlayAnchor;
  /** Width as a fraction of the output width. */
  widthPct?: number;
  /** Gap from the anchored edges, as fractions of output width and height.
   *  Ignored on a centred axis. */
  marginXPct?: number;
  marginYPct?: number;
  /** 0-1; 0 is treated as fully opaque. */
  opacity?: number;
}

/** A line of text burned into a rendition.
 *
 *  Every measurement is a FRACTION of the output (0-1), never pixels, for the
 *  reason RenditionOverlay gives. Size is a fraction of the output HEIGHT
 *  rather than width, because legibility is set by how tall a glyph is and a
 *  portrait tier would otherwise get unreadably small type. */
export interface RenditionText {
  /** The literal string drawn. Never interpreted: the filter is built with
   *  expansion=none, so a percent sign is a glyph rather than a directive. */
  content?: string;
  /** A BARE FILENAME in the fonts directory, never a path. Empty means the
   *  built-in default. Fetch the choices from GET /api/v1/fonts rather than
   *  hardcoding them -- operators add their own fonts to that directory. */
  font?: string;
  anchor?: OverlayAnchor;
  /** Type size as a fraction of the output HEIGHT. */
  sizePct?: number;
  /** An FFmpeg colour: a name, 0xRRGGBB, or either with @alpha. */
  color?: string;
  /** Gap from the anchored edges. Ignored on a centred axis. */
  marginXPct?: number;
  marginYPct?: number;
  /** A filled rectangle behind the text. It is what keeps white text readable
   *  over a white shirt. */
  box?: boolean;
  boxColor?: string;
  /** 0-1. A box at full opacity hides the picture behind it. */
  boxOpacity?: number;
}

/** One thing a platform may accept in a metadata push.
 *
 *  Mirrors oauth.AllMetadataFields. Kept HERE rather than in the page that
 *  renders it, because a push result names these in `applied` and `skipped` and
 *  the union is therefore an API contract, not a component detail.
 *
 *  TestUITypesCanNameEveryMetadataField in internal/oauth fails if the Go side
 *  gains a field this union does not have, AND if this union gains one no Go
 *  constant produces. A field the UI cannot name is one an operator sees as
 *  nothing at all. */
export type MetaField =
  // What the operator types.
  | "title"
  | "description"
  | "category"
  // What a platform requires them to declare. Pushed through the compliance
  // path rather than the composer, but a result still names them.
  | "privacy"
  | "madeForKids"
  | "contentLabels"
  // What lives on the broadcast rather than the channel. YouTube only, and
  // most of it stops being writable once a broadcast goes live.
  | "scheduledStart"
  | "contentDetails"
  | "tags";

/** One font available to a text overlay, from GET /api/v1/fonts. */
export interface FontInfo {
  name: string;
  /** polyemesis rewrites the built-ins on every startup, so replacing one is
   *  undone by a restart. Warn rather than forbid. */
  builtIn: boolean;
}

/** A rendition plus its usage. `enabledDestinations` is the ref count the
 *  engine acts on: at zero there is no process and no CPU burnt. */
export interface RenditionView {
  rendition: Rendition;
  destinations: number;
  enabledDestinations: number;
}

/** What deleting a rendition cost. The destinations are not deleted with it —
 *  they fall back to passthrough, which the warning spells out. */
export interface RenditionDeleted {
  status: string;
  destinations: number;
  enabledDestinations: number;
  warning?: string;
}

/** An editable starting point for the create form. Passthrough is the odd one
 *  out: it is the absence of a rendition, not a row. */
export interface RenditionPreset {
  key: string;
  label: string;
  passthrough: boolean;
  rendition?: Rendition | null;
}

/** Server-side validation limits, so the form's inputs cannot drift from the
 *  bounds the store actually enforces. */
export interface RenditionBounds {
  minDimension: number;
  maxDimension: number;
  maxFps: number;
  minBitrate: number;
  maxBitrate: number;
  minGopSeconds: number;
  maxGopSeconds: number;
}

/** The silicon behind an encoder. "software" is libx264/libx265, which drive
 *  no silicon at all. */
export type GpuVendor = "intel" | "nvidia" | "amd" | "apple" | "software" | "unknown";

/** One device the encoders could use — on Linux a /dev/dri node.
 *
 *  `usable` is the field that matters: a node that exists but cannot be opened
 *  is the most common hardware-encoding failure there is, and `problem` is
 *  already phrased as the fix. */
export interface GpuDevice {
  path: string;
  node: string;
  render: boolean;
  vendor: GpuVendor;
  vendorId?: string;
  usable: boolean;
  problem?: string;
}

/** What the machine has, as opposed to what the FFmpeg build lists. Advisory
 *  only — the test encode decides what works. `notes` are operator-facing and
 *  already written as instructions, so they are rendered verbatim. */
export interface GpuInfo {
  platform: string;
  devices?: GpuDevice[];
  vendors?: GpuVendor[];
  vaapiDevice?: string;
  nvidia: boolean;
  nvidiaDriver?: string;
  appleSilicon?: boolean;
  notes?: string[];
}

/** One choice in the encoder list.
 *
 *  `available` and `works` are two different questions and the gap between them
 *  is the whole point: a stock Linux FFmpeg is `available` for nvenc, qsv, vaapi
 *  and amf on a box with no GPU in it. `works` is the result of actually
 *  encoding a frame here, and `reason` is FFmpeg's own words when it did not. */
export interface EncoderInfo {
  name: string;
  codec: string;
  vendor: GpuVendor;
  hardware: boolean;
  /** Whether this FFmpeg build registers the encoder. */
  available: boolean;
  /** Whether it is usable on this machine. Unknown counts as usable —
   *  detection that could not run must not take choices away. */
  works: boolean;
  /** True when this exact encoder was test-encoded, false when the verdict was
   *  assumed or inherited from the H.264 encoder of the same family. */
  measured: boolean;
  reason?: string;
  durationMs?: number;
  /** The one a new rendition starts on. */
  default: boolean;
}

/** GET /encoders. `probed` is whether the build's encoder list was readable;
 *  `tested` is whether anything was actually encoded. Both false means every
 *  `works` below is an assumption. */
export interface EncoderList {
  encoders: EncoderInfo[];
  default: string;
  probed: boolean;
  tested: boolean;
  /** Working hardware encoders in preference order; empty means software only. */
  hardware: string[] | null;
  gpu: GpuInfo;
}

export type ProcessState =
  | "stopped"
  | "starting"
  | "running"
  | "reconnecting"
  | "failed";

export interface Progress {
  frame: number;
  fps: number;
  bitrateKbps: number;
  totalSize: number;
  outTimeMs: number;
  dupFrames: number;
  dropFrames: number;
  speed: number;
  done: boolean;
}

export interface ProcessStatus {
  name: string;
  kind: string;
  state: ProcessState;
  pid: number;
  restarts: number;
  startedAt?: string;
  uptimeSec: number;
  lastError?: string;
  nextRetryIn?: number;
  progress: Progress;
}

export interface DestStatus {
  id: number;
  name: string;
  kind: DestKind;
  platform: Platform;
  enabled: boolean;
  summary: string;
  tracks: number[] | null;
  filterComplex: string;
  normalization: NormalizeMode;
  warnings: string[] | null;
  error?: string;
  process?: ProcessStatus | null;
  /** The shared encode feeding this destination; absent for passthrough. */
  renditionId?: number | null;
  renditionName?: string;
  /** The pre-announced scheduled Facebook broadcast, when one exists. */
  facebookBroadcastId?: string;
}

/** One shared video encode's live state.
 *
 *  `consumers` is the ref count the engine acted on. A rendition with none has
 *  no process by design, so an absent `process` reads as idle, not as failed. */
export interface RenditionStatus {
  id: number;
  name: string;
  width: number;
  height: number;
  fps: number;
  videoBitrate: number;
  encoder: string;
  codec: string;
  consumers: number;
  relayPort?: number;
  error?: string;
  process?: ProcessStatus | null;
}

export interface RelayStats {
  port: number;
  subscribers: string[] | null;
  rxPackets: number;
  rxBytes: number;
  txPackets: number;
  dropped: number;
  /** MPEG-TS continuity counters, measured on the ingest side of the relay. */
  tsPackets: number;
  tsLost: number;
  discontinuities: number;
  lossPercent: number;
}

export interface Status {
  ingest?: ProcessStatus | null;
  recorder?: ProcessStatus | null;
  preview?: ProcessStatus | null;
  meters?: ProcessStatus | null;
  renditions: RenditionStatus[];
  destinations: DestStatus[];
  source: SourceInfo;
  relay: RelayStats;
}

/** peak[track][channel] and rms[track][channel], in dBFS. */
export interface Levels {
  peak: number[][] | null;
  rms: number[][] | null;
}

export interface SystemStats {
  cpuPercent: number;
  procCpuPercent: number;
  memUsedBytes: number;
  memTotalBytes: number;
  memPercent: number;
  procMemBytes: number;
  numCpu: number;
}

export interface BitrateSample {
  t: string;
  kbps: number;
}

/** `pull` inverts the direction: rather than waiting for an encoder, the server
 *  dials a source. That is what lets an IP camera, another server's HLS, or a
 *  looped file become the ingest. */
export type IngestMode = "srt" | "rtmp" | "pull";

/** The schemes the server will dial. Mirrors ffmpeg.PullSchemes(); the server
 *  rejects anything else, and quotes this same list when it does. */
export const PULL_SCHEMES = [
  "dash",
  "file",
  "hls",
  "http",
  "https",
  "rtmp",
  "rtmps",
  "rtsp",
  "rtsps",
  "srt",
] as const;

export const RTSP_TRANSPORTS = ["tcp", "udp", "udp_multicast", "http", "https"] as const;

export interface PullSettings {
  /** A `file://` source is a path RELATIVE to the data directory and may not
   *  contain "..": the server confines it there the same way file destinations
   *  are confined. */
  url: string;
  /** Caps FFmpeg's HTTP reconnect backoff. 0 uses the built-in default. */
  reconnectDelayMaxSeconds: number;
  /** Empty means TCP, which is right for almost every camera. */
  rtspTransport: string;
}

/** One ingested programme.
 *
 *  Multi-source exists because a horizontal and a vertical feed out of OBS's
 *  vertical-canvas plugin are two different compositions, not one cropped from
 *  the other. Each source carries its own ingest, and owns its own
 *  destinations and renditions. */
export interface Source {
  id: number;
  name: string;
  enabled: boolean;
  /** Same shape as Settings.ingest, deliberately: one form serves both. */
  ingest: Settings["ingest"];
  /** Publish secret. See SourceView.tokenEnforced before presenting this as
   *  a security control -- today it is stored but nothing checks it. */
  token: string;
  position: number;
  createdAt: string;
  updatedAt: string;
}

/** A source plus what the server computes about it. */
export interface SourceView extends Source {
  /** Ready to paste into an encoder, keyed by protocol, with `<server>` where
   *  the hostname goes. The token is deliberately NOT in these. */
  publishUrls: Record<string, string>;
  /** Whether unscoped API calls act on this source. */
  isDefault: boolean;
  /** Whether the publish token actually gates anything: true only while the
   *  one-port SRT listener is BOUND and serving this source. With per-source
   *  ports the token is inert and what protects the ingest is the RTMP stream
   *  key or the SRT passphrase. Follows the running listener, not the setting,
   *  because a listener that failed to bind enforces nothing. */
  tokenEnforced: boolean;
  /** Whether an encoder is live on the shared listener for this source. */
  publishing: boolean;
  /** What a delete would take with it. Counts, not prose: confirming a number
   *  is a decision, confirming "and its destinations" is a click. */
  destinations: number;
  renditions: number;
  /** Uplink health for that publisher, when there is one. */
  link?: {
    peer: string;
    since: string;
    bytes: number;
    rttMs: number;
    lossPackets: number;
    retransPackets: number;
  };
  /** Whether an engine actually came up. A source whose port was already taken
   *  is stored but not running, and that is the answer to "why is nothing
   *  arriving". */
  running: boolean;
}

export interface Settings {
  ingest: {
    mode: IngestMode;
    srt: { passphrase: string; latencyMs: number };
    rtmp: { app: string; streamKey: string };
    /** Optional so a client that predates pull can still PUT settings. */
    pull?: PullSettings;
  };
  /** Where the server binds. Install-wide: there is one SRT listener serving
   *  every source (told apart by token) and one RTMP listener serving at most
   *  one. Optional so a client that predates the change can still PUT. */
  listeners?: { srtPort: number; rtmpPort: number };
  /** Synthetic sources. Optional for the same reason. */
  synth?: {
    /** Synthesises a silent stereo track when the ingest probes with no audio
     *  at all. On by default: a video-only stream is refused by every major
     *  platform. It can never affect an ingest that does carry audio. */
    silenceOnVideoOnly: boolean;
  };
  recording: {
    enabled: boolean;
    segmentSeconds: number;
    maxGb: number;
    maxAgeHours: number;
    minFreeGb: number;
    /** One lossless file per ingest track, alongside the muxed recording.
     *  Named from the track roles, so a stem is `mic.flac` rather than
     *  `track3.flac`. Optional so a client that predates stems can still PUT. */
    stems?: boolean;
    /** flac or wav. Empty takes the server's default. */
    stemCodec?: StemCodec;
  };
  preview: {
    enabled: boolean;
    segmentSeconds: number;
    videoHeight: number;
    videoKbps: number;
    idleTimeoutSeconds: number;
  };
  meters: { enabled: boolean; intervalMs: number };
  logging: {
    persistProcessLogs: boolean;
    maxFileMb: number;
    maxFiles: number;
  };
  /** Optional so a client that predates playout can still PUT settings: the
   *  server merges over the stored value, and an absent key leaves it alone. */
  playout?: PlayoutSettings;
  /** The source-selector tier. Optional for the same reason. */
  failover?: FailoverSettings;
  /** Retained MQTT telemetry. Optional for the same reason. */
  mqtt?: MQTTSettings;
  /** Install-wide destination policy. Optional for the same reason. */
  destinations?: { staggerMs: number };
  /** How much chat scrollback is kept. Optional for the same reason.
   *
   *  This is the depth of the moderator's user card, not just a disk knob: that
   *  card answers "what has this person said before" out of polyemesis's own
   *  store, because no platform publishes a chat-history API. Set it too short
   *  and a card opened on a returning troublemaker reads as "they have never
   *  said anything" rather than "we did not keep it". */
  chat?: ChatRetentionSettings;
  /** Automatic chat moderation. Optional so a client that predates it can still
   *  PUT settings. See AutomodSettings below. */
  automod?: AutomodSettings;
  /** Alert delivery policy. Optional so a client that predates it can still
   *  PUT settings. See AlertSettings below. */
  alerts?: AlertSettings;
}

/** Bounds on the stored chat scrollback.
 *
 *  Both apply and the more generous one wins: a message goes when it is older
 *  than `retentionHours` AND outside the newest `keepMessages`. So a busy
 *  channel keeps less time than asked and a quiet one keeps more — the floor is
 *  what stops a slow channel's user cards being empty. */
export interface ChatRetentionSettings {
  /** 0 keeps forever, the same convention the recorder's maxAgeHours uses. */
  retentionHours: number;
  /** Newest-N floor, kept whatever their age. */
  keepMessages: number;
  /** How often the sweep runs. */
  purgeMinutes: number;
  /** Size of the in-memory ring a connecting browser reads before it falls
   *  back to querying the database.
   *
   *  Bounded far lower than `keepMessages` because the two live in different
   *  places: that one is rows on disk, paid for as they arrive, while this ring
   *  is allocated in full up front — the number is memory held on a silent
   *  channel exactly as on a busy one.
   *
   *  Optional so a client that predates it can still PUT settings. */
  historyMessages?: number;
}

/** Install-wide alert delivery policy. Per-rule matching lives on the rule. */
export interface AlertSettings {
  /** How many times one delivery is tried before it is given up on, the first
   *  try included.
   *
   *  The backoff curve underneath is deliberately not exposed: it was chosen
   *  against measured behaviour and nothing argues for changing it. What an
   *  operator gets to decide is how long a dead endpoint is chased before the
   *  alert is dropped, which depends on their endpoint rather than on us. */
  retryAttempts: number;
}

// ---------------------------------------------------------------- failover

export type StemCodec = "flac" | "wav";

/** What happens when the current source goes quiet and comes back. */
export type FailoverReturn = "manual" | "auto";

/** The still shown while no source is live.
 *
 *  A slate is the difference between a platform seeing a dead stream and a
 *  platform seeing a holding card, so it is the one part of failover that
 *  matters even to an install with a single source. */
export interface SlateSettings {
  enabled: boolean;
  /** A path INSIDE the data directory, e.g. `slate/holding.png`. Empty paints
   *  `color` instead, which is the fallback precisely because a flat colour
   *  has no file that can fail to open. */
  imagePath?: string;
  /** Any spelling FFmpeg's colour parser takes. Empty means black. */
  color?: string;
  videoKbps?: number;
  encoder?: string;
  preset?: string;
}

/** One standby input, used when the primary stops delivering. */
export interface FailoverBackup {
  mode?: IngestMode;
  srt?: { passphrase: string; latencyMs: number };
  rtmp?: { app: string; streamKey: string };
  pull?: PullSettings;
}

/** One entry in the failover playlist, in play order.
 *
 *  `upload` names a stored upload -- what Store.List reports as File.Name and
 *  what Store.Resolve accepts -- rather than a path or an id. See Go's
 *  db.PlaylistItem.Upload for why that distinction is a security boundary,
 *  not a style choice: the concat demuxer trusts every path it is given, and
 *  that is only safe while every path was chosen by this process. */
export interface PlaylistItem {
  upload: string;
}

/** An ordered list of uploads the selector can put on air when no encoder is
 *  delivering.
 *
 *  Deliberately smaller than SlateSettings: a playlist plays files that
 *  already have their own encoding, so unlike a slate it needs no encoder,
 *  preset, colour or bitrate of its own. */
export interface PlaylistSettings {
  enabled: boolean;
  items: PlaylistItem[];
}

/** One playlist item's readiness, as reported by GET /failover/playlist.
 *  `detail` is only populated for `attention` -- the reason a row needs one
 *  has to be visible, not just the fact that it is not "ready". */
export interface PlaylistItemStatus {
  upload: string;
  state: "ready" | "transcoding" | "attention";
  detail?: string;
}

export interface PlaylistStatus {
  ready: boolean;
  items: PlaylistItemStatus[];
}

export interface FailoverSettings {
  enabled: boolean;
  /** How long the live source may deliver nothing before the tier switches.
   *  Longer than a reconnect on purpose: an encoder re-establishing an RTMP
   *  connection is normal operation, not a failure. */
  graceSeconds?: number;
  /** `manual` keeps the backup on air until somebody switches back --
   *  the default, because an automatic return can flap. */
  return?: FailoverReturn;
  /** With `auto`, how long the primary must stay healthy before it is trusted
   *  again. */
  returnStableSeconds?: number;
  backup?: FailoverBackup;
  slate?: SlateSettings;
  /** A file playlist as a failover candidate beside the slate: something that
   *  plays, rather than something that just holds. */
  playlist?: PlaylistSettings;
}

// -------------------------------------------------------------------- mqtt

/** Retained MQTT telemetry.
 *
 *  polyemesis speaks MQTT 5.0 only -- the client library implements the 5.0
 *  specification and nothing earlier, so a broker pinned to 3.1.1 will not
 *  complete a connection at all. */
export interface MQTTSettings {
  enabled: boolean;
  /** mqtt://, mqtts://, ws:// or wss://. Credentials in the URL are REFUSED:
   *  a URL reaches log lines and `ps` output, and there is no taking it back. */
  brokerUrl?: string;
  username?: string;
  /** Read-only. The password itself is never returned by any API. */
  hasPassword?: boolean;
  /** Roots the topic tree. Separators are preserved, so `home/av` means two
   *  levels rather than one. */
  prefix?: string;
  /** Distinguishes two installs sharing one broker, and keys the Home
   *  Assistant device. */
  instance?: string;
  /** Must be unique on the broker. Empty derives one from `instance`. */
  clientId?: string;
  intervalSeconds?: number;
  keepAliveSeconds?: number;
  /** Accepts a self-signed broker certificate. Named for what it does. */
  tlsSkipVerify?: boolean;
  /** Publishes Home Assistant device-discovery payloads. */
  discovery?: boolean;
}

// ---------------------------------------------------------------- playout
//
// Playout is the viewer-facing origin: the stream packaged as public HLS (and
// optionally DASH) rather than relayed to another platform. It CONSUMES the
// rendition tier — a variant copies its rendition's video bit-for-bit — so
// adding a rung costs a muxer, never a second video encode.

export type PlayoutFormat = "hls" | "hls+dash";

/** One publicly served rung. `renditionId` null packages the ingest directly. */
export interface PlayoutVariant {
  name: string;
  enabled: boolean;
  renditionId?: number | null;
  /** Which ingest track this rung publishes. A viewer's player wants one
   *  stereo track; per-destination audio routing is untouched by this. */
  audioTrack: number;
}

export interface PlayoutSettings {
  enabled: boolean;
  /** Serves playlists and segments without an admin session. Off by default. */
  public: boolean;
  /** Sends CORS headers on the media so a player on another site can fetch it,
   *  and relaxes the frame headers on /watch so the embed renders. */
  allowCrossOrigin: boolean;
  format: PlayoutFormat;
  segmentSeconds: number;
  playlistSegments: number;
  /** 0 is live-only. */
  dvrWindowSeconds: number;
  maxDiskMb: number;
  audioKbps: number;
  sessionIdleSeconds: number;
  maxSessions: number;
  variants: PlayoutVariant[];
}

export interface PlayoutVariantStatus {
  name: string;
  renditionId?: number | null;
  audioTrack: number;
  running: boolean;
  error?: string;
  bandwidth: number;
  width?: number;
  height?: number;
  /** Relative to the playout root; prefix with `/playout/`. */
  playlist: string;
  manifest?: string;
  viewers: number;
  startedAt?: string;
}

export interface PlayoutAnalytics {
  viewers: number;
  byVariant: Record<string, number> | null;
  peak: number;
  peakAt?: string;
  /** Total sessions opened; a reconnect counts as a new one. */
  sessions: number;
  requests: number;
  /** New viewers that arrived with the table full. They are still served. */
  uncounted: number;
  idleSeconds: number;
  capacity: number;
}

export interface PlayoutUsage {
  bytes: number;
  files: number;
  limitBytes: number;
  /** The cap is below one playlist window — raise it or lower the bitrate. */
  overLimit: boolean;
  deleted: number;
}

export interface PlayoutStatus {
  enabled: boolean;
  public: boolean;
  master: string;
  format: PlayoutFormat;
  variants: PlayoutVariantStatus[] | null;
  analytics: PlayoutAnalytics;
  usage: PlayoutUsage;
}

/** How an anonymous viewer proves they may watch. `token` is the default and
 *  the only safe one; `open` means anyone with the URL. */
export type PlayoutProtection = "token" | "open";

export interface PlayoutUrls {
  master: string;
  watch: string;
  embed: string;
}

/** The Playout page's single read. `token` is the playback secret in the
 *  clear — this response is behind the admin session. */
export interface PlayoutAdminView {
  status: PlayoutStatus;
  settings: PlayoutSettings;
  protection: PlayoutProtection;
  token: string;
  title: string;
  description: string;
  urls: PlayoutUrls;
  /** True only when anyone on the internet holding the URL can watch. The
   *  page leads with this. */
  exposed: boolean;
  /** False when the playout manager is not wired up, so the page can say so
   *  rather than showing an empty ladder. */
  running: boolean;
}

/** What the public player page is told. Never carries the token. */
export interface PlayoutPublicView {
  enabled: boolean;
  title: string;
  description: string;
  master: string;
  poster: string;
  variants: { name: string; playlist: string; width?: number; height?: number }[] | null;
  viewers: number;
}

export interface Recording {
  id: number;
  filename: string;
  startedAt: string;
  finishedAt: string;
  bytes: number;
  durationMs: number;
  tracks: number;
}

export interface DiskUsage {
  usedBytes: number;
  freeBytes: number;
  totalBytes: number;
  count: number;
  storage: StorageState;
}

/** The free-space guard's verdict on whether the volume can take more footage. */
export interface StorageState {
  halted: boolean;
  reason?: string;
}

/** A long-lived automation credential. The secret exists only at creation. */
export interface ApiToken {
  id: number;
  name: string;
  prefix: string;
  createdAt: string;
  lastUsedAt: string;
}

export interface FFmpegTools {
  ffmpeg: string;
  ffprobe: string;
  version: string;
  major: number;
  minor: number;
  hasLibsrt: boolean;
  hasLibx264: boolean;
  /** Every video encoder the binary registers. The candidate set, not the
   *  answer: this is what the BUILD was compiled with. */
  videoEncoders?: string[] | null;
  /** The hardware subset that passed its test encode, in preference order. */
  hwEncoders?: string[] | null;
  /** What each candidate did when this machine was asked to encode a frame.
   *  Empty means the test encode never ran. */
  encoderCaps?: EncoderCapability[] | null;
  /** Every filter the binary registers.
   *
   *  A filter is as optional as an encoder and fails just as hard. drawtext
   *  needs --enable-libfreetype and is genuinely missing from ordinary builds,
   *  so a feature that needs it has to check rather than assume. Empty means
   *  the probe never ran, which everything reads as "assume the best". */
  filters?: string[] | null;
}

/** One encoder's measured answer: what happened when this machine, with these
 *  drivers, was asked to encode a frame just now. */
export interface EncoderCapability {
  name: string;
  vendor: GpuVendor;
  works: boolean;
  reason?: string;
  durationMs: number;
}

export interface SystemInfo {
  version: string;
  ffmpeg: FFmpegTools;
  /** What the machine has. It rides alongside `ffmpeg` because the two are only
   *  meaningful together — an encoder list without the hardware behind it is
   *  what made the editor offer NVENC on an AMD box. */
  gpu: GpuInfo;
  ingestUrl: string;
  ingestMode: string;
  maxTracks: number;
  tlsEnabled: boolean;
  dataDir: string;
  uiBuilt: boolean;
}

// ----------------------------------------------------- music-rights policy
//
// A superset of `Platform`: the rights table folds a destination's platform
// and its kind together, because where the bytes land is the only thing a
// music policy cares about. A local recording is `file` no matter what
// platform the row claims. Kept separate from `Platform` so widening this
// cannot widen the destination editor's platform picker.
export type PolicyPlatform = Platform | "facebook" | "file";

/** "" follows the platform table; the other two overrule it. */
export type MusicPolicyChoice = "" | "keep" | "exclude";

/** The resolved answer to "does this destination carry music?", pre-phrased
 *  for the badge. `reason` names the mechanism ("Twitch DMCA policy"), never a
 *  legal conclusion, and `overridden` records that a person decided rather
 *  than the table. */
export interface MusicDecision {
  platform: PolicyPlatform;
  exclude: boolean;
  overridden: boolean;
  reason: string;
  summary: string;
}

/** The `platform:` prefix routing.PlatformPresetID() puts on the presets
 *  generated from the music-rights table. */
export const PLATFORM_PRESET_PREFIX = "platform:";

export interface Preset {
  id: string;
  name: string;
  description: string;
  needsMusicTrack: boolean;
  needsMicTrack: boolean;
  needsSurroundTrack: boolean;
  needsCleanTrack: boolean;
  /** Asks for a BCP-47 tag, e.g. "es". */
  needsLanguage: boolean;
  /** Set only on the presets generated from the music-rights table. Those
   *  double as the UI's copy of that table — there is no separate endpoint for
   *  it, and deriving the badge from the same rows the presets came from is
   *  what keeps the two from disagreeing. */
  platform?: PolicyPlatform;
  policy?: MusicDecision;
  loudness?: Loudness | null;
  delayMs?: number;
}

export interface PresetOpts {
  musicTrack: number;
  micTrack: number;
  surroundTrack: number;
  cleanTrack: number;
  language?: string;
  musicPolicy?: MusicPolicyChoice;
}

export interface PlatformCreds {
  platform: Platform;
  clientId: string;
  hasSecret: boolean;
  updatedAt: string;
}

export interface PlatformAccount {
  id: number;
  platform: Platform;
  accountName: string;
  accountRef: string;
  expiresAt: string;
  scopes: string;
  /** The provider's scope version when this account was connected. Compared
   *  against the running build's to spot a token issued before a permission
   *  was added. 0 means the row predates the field. */
  scopeVer: number;
  createdAt: string;
  updatedAt: string;
  /** Whether this account still holds the permissions this build needs.
   *
   *  Computed per request rather than stored, because the answer changes when
   *  the BINARY changes, not when the row does — upgrading polyemesis can turn
   *  a fine account into one that needs reconnecting. */
  reconnect?: ReconnectReason;
}

/** Why an account should be reconnected.
 *
 *  An OAuth token carries exactly the scopes it was issued with, and granting a
 *  new scope never upgrades a token that already exists. */
export interface ReconnectReason {
  needed: boolean;
  reason?: string;
  /** Named scopes the stored grant lacks, when that is how the verdict was
   *  reached. Absent when the verdict came from the version. */
  missing?: string[];
}

export interface SetupGuide {
  platform: Platform;
  name: string;
  consoleUrl: string;
  redirectPath: string;
  steps: string[];
  scopes: string[] | null;
  supported: boolean;
  /** Present when the account connects but the stream key is pasted by hand.
   *  The Go struct has carried this since the Kick stream-key work; this type
   *  never gained it. */
  manualStreamKey?: boolean;
  note?: string;
  /** Computed per request by the API, from the configuration and the Host it
   *  was reached on: reasons the displayed redirect URI may not work. */
  redirectWarnings?: string[];
}

/** The verdict on a pasted client ID and secret.
 *
 *  Four states rather than a boolean, because "we could not reach the platform"
 *  and "the platform said no" are different facts with different fixes -- and
 *  because YouTube cannot be checked at all. Rendering its format check as the
 *  same tick Twitch earns would be a lie told by a progress indicator. */
export interface CredentialCheck {
  platform: Platform;
  state: "verified" | "unverified" | "rejected" | "unreachable";
  method: "client_credentials" | "format";
  detail: string;
}

export interface LogLine {
  time: string;
  process: string;
  text: string;
  level: "info" | "warning" | "error" | "fatal";
}

export interface ProcessInfo {
  status: ProcessStatus;
  command: string;
}

/** The five values tls.mode accepts in config.yaml. `auto` only ever appears
 *  as `TlsStatus.configured`; `TlsStatus.mode` is always already resolved. */
export type TlsMode = "auto" | "acme" | "selfsigned" | "manual" | "off";

/** The public half of the certificate the server presents. Deliberately has no
 *  field for key material and must never grow one. */
export interface CertInfo {
  subject: string;
  issuer: string;
  dnsNames: string[];
  ipAddresses: string[];
  notBefore: string;
  notAfter: string;
  /** Negative once expired, so "expired 3 days ago" needs no second field. */
  daysRemaining: number;
  expired: boolean;
  /** SHA-256 of the DER, colon-separated uppercase hex. */
  fingerprint: string;
  selfSigned: boolean;
}

export interface TlsStatus {
  /** What `auto` decided, or the mode as written when it was not `auto`. */
  mode: TlsMode;
  configured: TlsMode;
  hostname: string;
  servesTls: boolean;
  trustProxyHeaders: boolean;
  /** Whether HSTS may be sent. False plus a warning is the interesting case:
   *  it means the operator asked for it somewhere it would be unsafe. */
  hsts: boolean;
  hstsWarning: string;
  /** Null when TLS is off, or before ACME's first issuance. */
  certificate: CertInfo | null;
  certificateError: string;
  caAvailable: boolean;
  caFingerprint: string;
}

export type EventType =
  | "status"
  | "levels"
  | "log"
  | "stats"
  | "source"
  | "recordings"
  /** One chat message, one event. See ChatMessage. */
  | "chat"
  /** The whole per-platform connection table, whenever any part of it moves. */
  | "chatState"
  /** Messages that were delivered and are now gone — deleted here, or deleted
   *  on the platform's own dashboard and reported back to us. See ChatRetraction.
   *
   *  The one event in this union that must not be dropped on the floor. Missing
   *  a "chat" costs a line of conversation; missing this one leaves a message on
   *  screen that a moderator deliberately removed. */
  | "chatRetract";

export interface WsEvent {
  type: EventType;
  time: string;
  data: unknown;
}

// --------------------------------------------------------------------- chat
//
// One pane, one send box, four platforms. Everything below mirrors
// internal/chat exactly; nothing here re-derives a fact the server already
// stated, because two answers to "is YouTube connected" is one answer too many.

/** The platforms the chat pane can show. Identical to `Platform` now that
 *  Facebook has a destination platform of its own, and kept as a separate name
 *  because chat and destinations gain platforms at different times — the next
 *  chat-only platform widens this without touching the destination picker. */
export type ChatPlatform = Platform;

/** A chat connection's condition, in the words the operator would use.
 *  `degraded` is running-but-limited and always arrives with a reason. */
export type ChatState =
  | "connecting"
  | "live"
  | "degraded"
  | "failed"
  | "stopped";

/** One badge the platform put next to a name. Deliberately loose: Twitch has
 *  id/version pairs, Kick has labels, YouTube has boolean roles. */
export interface ChatBadge {
  id: string;
  version?: string;
  label?: string;
}

/** One inline image, located by RUNE offsets into `text`. `start` is inclusive
 *  and `end` is exclusive, which is what both Go and JavaScript slice with. */
export interface ChatEmote {
  id: string;
  name?: string;
  start: number;
  end: number;
  url?: string;
}

export interface ChatAuthor {
  id?: string;
  name: string;
  /** "#rrggbb", or absent when the platform sent none — then the UI picks. */
  color?: string;
  badges?: ChatBadge[] | null;
  moderator?: boolean;
  subscriber?: boolean;
  broadcaster?: boolean;
}

export interface ChatMessage {
  id: string;
  platform: ChatPlatform;
  /** The platform account this arrived on. Two Twitch channels connected at
   *  once are two accounts, and merging them is a bug rather than a feature. */
  account?: string;
  channel?: string;
  author: ChatAuthor;
  text: string;
  emotes?: ChatEmote[] | null;
  at: string;
  /** An /me message: rendered in the author's colour, without a colon. */
  action?: boolean;
  replyToId?: string;
  replyTo?: string;
  /** Sent by polyemesis itself. */
  echo?: boolean;
}

/** YouTube's daily API budget. Present only where a platform has one that can
 *  silently kill chat for the rest of the day. `estimated` is always true and
 *  is in the payload so the UI can say so. */
export interface ChatQuota {
  used: number;
  limit: number;
  remaining: number;
  resetAt: string;
  intervalMs: number;
  paused?: boolean;
  estimated: boolean;
}

export interface ChatStatus {
  platform: ChatPlatform;
  account?: string;
  channel?: string;
  state: ChatState;
  /** One sentence saying WHY, for every state that is not plainly `live`. */
  detail?: string;
  since: string;
  received: number;
  sent: number;
  /** Reconnections. A number that climbs while the state reads `live` is a
   *  flapping connection, which looks healthy at every instant you check it. */
  restarts: number;
  lastError?: string;
  canSend: boolean;
  quota?: ChatQuota | null;
}

/** One platform's outcome from a fan-out send. `skipped` is "this platform
 *  cannot send", which is a permanent property, not a failure to retry. */
export interface ChatSendResult {
  platform: ChatPlatform;
  account?: string;
  ok: boolean;
  skipped?: boolean;
  detail?: string;
}

export interface ChatSendResponse {
  results: ChatSendResult[];
  sent: number;
  failed: number;
  skipped: number;
}

export interface ChatStats {
  received: number;
  deduped: number;
  stored: number;
  /** Shed because persistence fell behind. They were still shown live; only
   *  the scrollback lost them. */
  dropped: number;
  /** Withdrawn after delivery — deleted here, or deleted on the platform's own
   *  dashboard and reported back. A number climbing while nobody is using the
   *  pane's delete button means moderation is happening somewhere polyemesis
   *  cannot see. */
  retracted: number;
  pending: number;
  adapters: number;
}

/** What a moderation call did, in the server's own words.
 *
 *  `detail` is not decoration and must be shown. It carries the difference
 *  between "hidden from viewers" and "hidden only here, everyone can still see
 *  it" — a distinction the status field alone cannot make, and one a moderator
 *  who gets it wrong acts on for the rest of the broadcast. */
export interface ChatModerationResult {
  status: string;
  scope?: string;
  detail?: string;
}

/** Channel-wide chat rules. Only Twitch publishes an API for these.
 *
 *  Every field is optional and an omitted one means LEAVE IT ALONE, not "off".
 *  Twitch takes all of these in one body, so sending a full object would switch
 *  off follower-only mode as a side effect of adjusting slow mode. */
export interface ChatSettings {
  slowMode?: boolean;
  slowModeSeconds?: number;
  followerMode?: boolean;
  followerModeMinutes?: number;
  subscriberMode?: boolean;
  emoteMode?: boolean;
  uniqueChatMode?: boolean;
  nonModeratorChatDelay?: boolean;
  nonModeratorChatDelaySeconds?: number;
}

/** One viewer's recent messages and roles — the moderator's user card.
 *
 *  Read from polyemesis's own scrollback, because NO platform publishes an API
 *  for a viewer's chat history. Twitch's mod card is a Twitch web-app feature
 *  over internal endpoints; Helix offers "who is here now" and "who are the
 *  moderators", neither of which is a history. The others have nothing.
 *
 *  Being local makes it work identically on all four platforms, which Twitch's
 *  own card cannot do. The cost is depth, which is why `truncated` and
 *  `retentionNote` exist and must be rendered: a moderator who reads a bounded
 *  window as a complete record judges a pattern from a sample. */
export interface ChatUserCard {
  platform: ChatPlatform;
  authorId: string;
  /** As they appeared when they last spoke, not a fresh platform lookup. */
  name?: string;
  color?: string;
  moderator?: boolean;
  subscriber?: boolean;
  broadcaster?: boolean;
  messages: ChatMessage[];
  /** The limit was reached, so the count is a floor and not a total. */
  truncated: boolean;
  /** Why the history is only this deep. Show it. */
  retentionNote: string;
}

/** Messages that are gone. Carried by the "chatRetract" event.
 *
 *  A list rather than one id because a timeout removes everything one author
 *  said, and one event per message would let the pane render a half-applied
 *  timeout — some of the offender's messages gone, some still there. */
export interface ChatRetraction {
  platform: ChatPlatform;
  account?: string;
  /** What the server was actually holding and has dropped. NOT necessarily
   *  everything the platform removed: anything already out of the server's
   *  history ring cannot be named. */
  messageIds: string[];
  /** Set when the platform named a user rather than a message, so a client
   *  holding its own longer buffer can apply the same rule to messages the
   *  server no longer has. */
  authorId?: string;
  /** The platform cleared the entire room. */
  all?: boolean;
}

/** A platform's published maximum message length. Advisory: the composer warns
 *  and still sends, because the platform is the authority on its own rules and
 *  a limit we got wrong would cost the operator a message for nothing. */
export interface ChatLimit {
  platform: ChatPlatform;
  maxChars: number;
}

export interface ChatOverview {
  /** False when no chat hub is wired at all — distinct from a hub running with
   *  nothing attached, because the operator's next move differs. */
  configured: boolean;
  statuses: ChatStatus[];
  stats?: ChatStats | null;
  messages: ChatMessage[];
  limits: ChatLimit[];
  /** The scrollback came from the database rather than a live connection. */
  stored?: boolean;
}

/** Per-destination muxer and socket tuning.
 *
 *  Everything here was probed against the pinned FFmpeg before it shipped, and
 *  every field is off by default: a destination that has not opted in produces
 *  exactly the command it always did. */
export interface DestTransport {
  /** Drops FLV's zero duration and filesize metadata. Some RTMP ingests treat
   *  a zero duration as a malformed file rather than as a live stream. RTMP
   *  only; ignored on every other kind. */
  noDurationFilesize?: boolean;
  /** The interleave buffer. These are a PAIR: FFmpeg applies the packet cap
   *  only once the queue has passed the byte threshold, so a threshold with no
   *  cap does nothing at all and the server refuses it. */
  muxQueuePackets?: number;
  muxQueueBytes?: number;
  /** Breaks a half-open socket. Without it a far end that vanished without a
   *  FIN blocks the muxer indefinitely: FFmpeg keeps running, the supervisor
   *  sees a live process, and the stream is off air with nothing reporting
   *  it. 0 disables. */
  rwTimeoutSeconds?: number;
}

/** How hard a destination is retried, and when to stop.
 *
 *  The zero value is the behaviour every destination had before this existed:
 *  retry forever, 1s to 30s. */
export interface DestResilience {
  minBackoffSeconds?: number;
  maxBackoffSeconds?: number;
  /** Stop after this many CONSECUTIVE failed restarts; 0 retries forever.
   *
   *  The point is not to save CPU. A destination retrying forever is
   *  indistinguishable from one that works -- the card says "reconnecting",
   *  and nothing ever says this endpoint is not coming back. Giving up moves
   *  it to failed, which the alert rules already treat as an incident. */
  giveUpAfter?: number;
}

/** Per-destination audio encoding.
 *
 *  Note what is NOT here: an AAC profile. FFmpeg's native AAC encoder supports
 *  only LC, and refuses to open at all when asked for HE-AAC; HE-AAC needs the
 *  nonfree libfdk_aac. Opus answers the same goal -- good audio well below
 *  64 kbps -- and is already in the build. */
export interface AudioEncoding {
  /** Empty is AAC. "opus" is refused on RTMP: FFmpeg will mux it, but no
   *  mainstream RTMP ingest accepts it, so the stream would upload cleanly and
   *  be rejected. */
  codec?: "" | "opus";
  /** Folds the routing graph's stereo output to one channel. A downmix of your
   *  mix, not a re-route. */
  mono?: boolean;
}

/** YouTube broadcast visibility. Empty means LEAVE IT ALONE, and that
 *  distinction matters: YouTube's update is destructive by PART, so a status
 *  write that omits privacyStatus reverts the broadcast to its default rather
 *  than leaving it. */
export type PrivacyStatus = "" | "private" | "unlisted" | "public";

/** Twitch content classification labels Twitch will WRITE.
 *
 *  MatureGame is deliberately absent: it is readable and not writable, so
 *  offering it would be a control that silently never applies. */
export const TWITCH_LABELS = [
  "DebatedSocialIssuesAndPolitics",
  "DrugsIntoxication",
  "Gambling",
  "ProfanityVulgarity",
  "SexualThemes",
  "ViolentGraphic",
] as const;

/** Obligation metadata: who the programme is for, who may see it, what a
 *  viewer is about to be shown. Every zero value means "do not touch". */
export interface Compliance {
  privacy?: PrivacyStatus;
  /** COPPA self-declaration. undefined is "not said"; false is the real
   *  declaration "this is not for children", and the two are different. */
  madeForKids?: boolean;
  /** Twitch labels, id -> enabled. A key set to false actively CLEARS it. */
  labels?: Record<string, boolean>;
  /** Facebook's audience for a live video. undefined leaves it alone. */
  facebookPrivacy?: string;
}

/** One Page a Facebook broadcast crossposts to at create time, and whether it
 *  also gets a post published as that Page rather than only being shared to.
 *  pageId is the opaque numeric id Facebook's own console shows — there is no
 *  lookup, so the editor cannot offer anything friendlier than that. */
export interface CrosspostTarget {
  pageId: string;
  createPost?: boolean;
}

/** Facebook create-time-only settings: separate from Compliance because
 *  neither field is an obligation the platform imposes — they are choices
 *  that only apply at the moment the broadcast is created. See the DB layer's
 *  db.FacebookSettings, which this mirrors field for field. */
export interface FacebookSettings {
  crosspost?: CrosspostTarget[];
  donateCharityId?: string;
  /**
   * The occurrence a broadcast has already been announced for. A time rather
   * than a flag, because a weekly show needs a new broadcast every week.
   */
  scheduledFor?: string;
  /** The Facebook live video created for it — what the card links to. */
  broadcastId?: string;
  /** Publishes a redundant feed to Facebook's backup ingest endpoint. Doubles
   *  this destination's upload bandwidth; enabling it reconnects the stream
   *  once, because a backup endpoint only exists on a broadcast created with
   *  one. */
  backupIngest?: boolean;
}

// ------------------------------------------------------------------- expert
//
// Hand-edited FFmpeg arguments, per destination. Two strings appended to the
// generated command — one before the input, one before the output — with
// everything else, including the audio routing graph, untouched.

/** One override the server wants said out loud before it will save. */
export interface ExpertGuard {
  arg: string;
  reason: string;
}

/** The exact argv a destination would run, rendered for reading. */
export interface ResolvedCommand {
  bin: string;
  argv: string[];
  /** True when this came from the destination's RUNNING process, so every
   *  value in it is real. False means it was rebuilt and `note` says which
   *  parts are stand-ins. */
  command: string;
  live: boolean;
  note?: string;
}

export interface ExpertArgs {
  inputArgs: string;
  outputArgs: string;
  ackReencode: boolean;
}

export interface ExpertResponse {
  destinationId: number;
  args: ExpertArgs;
  enabled: boolean;
  command: ResolvedCommand;
  guards?: ExpertGuard[];
  passthrough: boolean;
  applied: boolean;
  warning?: string;
}

/** Three-valued on purpose. An unreachable ingest is not a verdict on the
 *  arguments, and reporting it as one would refuse an edit for a reason that
 *  has nothing to do with it. */
export interface DryRunResult {
  verdict: "ok" | "invalid" | "inconclusive";
  message?: string;
  command: string;
  output?: string;
}

// ------------------------------------------------------- background jobs
//
// Everything heavy in this product is a queued job governed by a resource
// policy that yields to the live stream by default. Transcription, proxy
// encodes and archive compression all cost CPU, and a dropped frame on a live
// broadcast is unrecoverable while a transcript arriving an hour later costs
// nothing. These types are how the operator sees and steers that tradeoff.

export type JobState =
  | "queued"
  | "running"
  | "done"
  | "failed"
  | "cancelled"
  | "deferred";

/** How one kind of work is allowed to take the machine.
 *  - realtime:  never held back
 *  - deferred:  yields to the stream (the default, and the whole point)
 *  - scheduled: only inside its windows
 *  - manual:    only when a human releases it */
export type JobMode = "realtime" | "deferred" | "scheduled" | "manual";

export const JOB_MODES = ["realtime", "deferred", "scheduled", "manual"] as const;

export const JOB_MODE_LABEL: Record<JobMode, string> = {
  realtime: "Realtime",
  deferred: "Yield to stream",
  scheduled: "Scheduled",
  manual: "Manual only",
};

export const JOB_MODE_HINT: Record<JobMode, string> = {
  realtime: "Runs immediately, even while a broadcast is going out. Only for work you know is cheap.",
  deferred: "Waits for the ingest to stop and for the machine to be quiet. The safe default.",
  scheduled: "Only runs inside the windows below — overnight, typically.",
  manual: "Never starts on its own. You release each job by hand.",
};

/** Priority is stored as a number; these are the three the API mints. */
export type JobPriority = number;

export interface Job {
  id: number;
  kind: string;
  /** Opaque to the queue; "recording:<id>" for everything this UI submits. */
  target: string;
  params?: unknown;
  result?: unknown;
  priority: JobPriority;
  state: JobState;
  unique?: boolean;
  /** Counts STARTS, not failures. */
  attempts: number;
  maxAttempts: number;
  /** 0..1. */
  progress: number;
  log?: string[];
  /** Survives a retry, so a job going wrong is visible before it gives up. */
  error?: string;
  createdAt: string;
  availableAt?: string;
  startedAt?: string;
  finishedAt?: string;
  updatedAt: string;
}

/** A job plus the two things the queue does not store: why it is not running,
 *  and roughly how long it has left. `reason` is the load-bearing field on the
 *  jobs page — a paused job with no explanation reads as a broken job. */
export interface JobView extends Job {
  recordingId?: number;
  recording?: string;
  blocked: boolean;
  reason?: string;
  etaSeconds?: number;
  label?: string;
}

export interface JobStats {
  running: number;
  paused: boolean;
  started: number;
  completed: number;
  failed: number;
  retried: number;
  cancelled: number;
  requeued: number;
  byKind?: Record<string, number>;
}

/** A local wall-clock range a scheduled kind may run in. It may wrap midnight,
 *  and a wrapping window belongs to the day its START falls on. */
export interface JobWindow {
  /** IANA zone. Empty means UTC — never the server's local time. */
  tz?: string;
  /** Minutes past local midnight. End may be 1440 for the following midnight. */
  startMinutes: number;
  endMinutes: number;
  /** 0 = Sunday. Empty means every day. */
  days?: number[];
}

export interface JobKindInfo {
  kind: string;
  label: string;
  description: string;
  mode: JobMode;
  windows?: JobWindow[];
  usesGpu: boolean;
  ignoreIngest: boolean;
  /** Whether this kind has a policy row of its own, or inherits the default. */
  overridden: boolean;
  /** Fails open: a kind we cannot judge is available. */
  available: boolean;
  unavailable?: string;
}

export interface PowerState {
  /** False means the platform told us nothing, which gates nothing. */
  known: boolean;
  onBattery: boolean;
  /** -1 when unknown. */
  percent: number;
  tempC: number;
}

export interface GovernorGates {
  at: string;
  /** Includes the linger period after the stream stopped. */
  ingestLive: boolean;
  /** -1 when unavailable. */
  cpuPercent: number;
  cpuOverCeiling: boolean;
  cpuSustained: boolean;
  gpuBusy: boolean;
  onBattery: boolean;
  tooHot: boolean;
  power: PowerState;
}

export interface GovernorVerdict {
  kind: string;
  mode: JobMode;
  /** May a queued job of this kind be claimed now. */
  start: boolean;
  /** May one that is ALREADY running keep the machine. */
  continue: boolean;
  suspension: "stop" | "finish-then-yield" | string;
  reason: string;
}

export interface GovernorSnapshot {
  at: string;
  enabled: boolean;
  gates: GovernorGates;
  verdicts: GovernorVerdict[];
  deferred?: number[];
  suspended?: string[];
  /** Kinds that should be paused but cannot be, and are finishing instead. */
  yielding?: string[];
  paused: boolean;
}

export interface PostProdKindSettings {
  kind: string;
  mode?: JobMode | "";
  windows?: JobWindow[];
  usesGpu?: boolean;
  ignoreIngest?: boolean;
}

export interface PostProdSettings {
  enabled: boolean;
  concurrency: number;
  defaultMode: JobMode | "";
  yieldToStream: boolean;
  cpuCeilingPercent: number;
  cpuResumePercent: number;
  cpuSustainedSeconds: number;
  cpuSettleSeconds: number;
  avoidGpuWhenStreaming: boolean;
  gpuBusy: boolean;
  batteryFloorPercent: number;
  thermalCeilingC: number;
  niceLevel: number;
  idleIo: boolean;
  ingestLingerSeconds: number;
  deferSeconds: number;
  retainDays: number;
  retainJobs: number;
  kinds?: PostProdKindSettings[];
  /** Transcription model for jobs that name none. Empty keeps the
   *  hardware-derived choice, which is the right default and stays it.
   *
   *  Model choice IS the transcription decision — speed against accuracy
   *  against memory — and the right answer depends partly on hardware
   *  polyemesis can measure and partly on how much you care about the
   *  transcript, which it cannot. See WhisperInfo.models for what this machine
   *  has. */
  whisperModel?: string;
}

/** What this machine can do about speech to text. Reported even when
 *  whisper.cpp is absent: "install this and the button appears" beats a
 *  disabled control with no explanation. */
export interface WhisperInfo {
  available: boolean;
  unavailable?: string;
  binary?: string;
  version?: string;
  backends?: string[];
  backend?: string;
  models?: string[];
  defaultModel?: string;
  realtime: boolean;
  realtimeNote?: string;
}

export interface JobsOverview {
  /** False means no queue is wired on this server. */
  available: boolean;
  paused: boolean;
  stats: JobStats;
  counts: Record<string, number>;
  governor?: GovernorSnapshot;
  policy: PostProdSettings;
  kinds: JobKindInfo[];
  active: JobView[];
  recent: JobView[];
  whisper: WhisperInfo;
}

// ------------------------------------------------------------- the library
//
// Sessions are the primary unit: a session is one broadcast, and its segments
// are an implementation detail of how the recorder wrote it down.

export interface RecordingAssets {
  proxy: boolean;
  poster: boolean;
  contactSheet: boolean;
  sprites: boolean;
  archive: boolean;
}

export interface LibraryRecording extends Recording {
  sessionId?: number;
  title?: string;
  description?: string;
  tags?: string[];
  hasTranscript: boolean;
  assets: RecordingAssets;
  activeJobs?: JobView[];
}

export interface Session {
  id: number;
  title: string;
  description: string;
  tags: string[] | null;
  startedAt: string;
  endedAt: string;
  durationMs: number;
  bytes: number;
  recordings: number;
  /** False once a human has built or split this session by hand. */
  auto: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface LibrarySession extends Session {
  displayTitle: string;
  /** The derived still that stands in for the session, if one has been
   *  generated. Keyed on the recording id, which is what the media URL wants. */
  posterRecordingId?: number;
  posterFile?: string;
  transcribed: number;
}

export interface LibraryView {
  sessions: LibrarySession[];
  ungrouped: LibraryRecording[];
  tags: string[];
  speakers: string[];
  jobsAvailable: boolean;
  transcribeAvailable: boolean;
  transcribeNote?: string;
  /** The sentinels the search wraps matched terms in. Split on these. */
  markers: [string, string];
}

/** The editable half of a session or a recording. Kept separate from the
 *  computed span so an update cannot carry a stale one back. */
export interface Metadata {
  title: string;
  description: string;
  tags: string[];
}

export interface TranscriptSegment {
  id: number;
  recordingId: number;
  track: number;
  speaker?: string;
  startMs: number;
  endMs: number;
  text: string;
  confidence?: number;
  confidenceKnown?: boolean;
}

export interface TranscriptTrack {
  id: number;
  recordingId: number;
  track: number;
  speaker?: string;
  role?: string;
  language?: string;
  model?: string;
  backend?: string;
  createdAt: string;
  segments?: TranscriptSegment[];
  count: number;
  durationMs: number;
}

export interface Transcript {
  recordingId: number;
  recording: string;
  tracks: TranscriptTrack[];
}

export interface TranscriptView {
  transcript: Transcript;
  /** The free-diarization view: time-ordered, already speaker-attributed
   *  because each microphone was recorded on its own track. */
  merged: TranscriptSegment[];
  speakers: string[];
  segments: number;
}

export type TranscriptOrder = "relevance" | "time" | "recent";

export interface TranscriptHit {
  segmentId: number;
  recordingId: number;
  recording: string;
  sessionId?: number;
  track: number;
  speaker?: string;
  startMs: number;
  endMs: number;
  /** Wall-clock instant of the utterance, not an offset. */
  at: string;
  text: string;
  /** Text with matched terms wrapped in the markers. */
  snippet: string;
  context?: string;
  score: number;
}

/** Everything the transcript search accepts. Every field is optional but the
 *  text, and the server clamps the numbers, so a caller may pass what it has. */
export interface SearchParams {
  q: string;
  /** Makes the final term a prefix match, which is what makes
   *  search-as-you-type useful. */
  prefix?: boolean;
  /** Passes the text to FTS5 untouched, for someone who knows the syntax. */
  raw?: boolean;
  recordingId?: number;
  sessionId?: number;
  track?: number;
  speaker?: string;
  /** RFC3339 or a bare YYYY-MM-DD. */
  since?: string;
  until?: string;
  order?: TranscriptOrder;
  limit?: number;
  offset?: number;
  /** Neighbouring segments glued either side of the hit; negative for none. */
  context?: number;
  snippetTokens?: number;
}

export interface SearchResults {
  hits: TranscriptHit[];
  /** -1 when the count could not be taken; the hits are still good. */
  total: number;
  limit: number;
  offset: number;
  markers: [string, string];
}

/** A media file the server holds.
 *
 *  `origin` says where it came from and is DERIVED server-side from which store
 *  the item was read out of, never stored beside it — a row in the recordings
 *  table is something the server captured, by construction. */
export type MediaOrigin = "recorded" | "uploaded" | "clip";

export interface MediaFile {
  name: string;
  origin: MediaOrigin;
  bytes: number;
  modified: string;
  /** Paste into a pull source. Relative to the data directory, which is what
   *  ffmpeg's file:// handling resolves against. */
  pullUrl: string;
}

/* --------------------------------------------------------------- automod --
 *
 * Automatic chat moderation. See docs/roadmap/CHAT-AUTOMOD.md.
 *
 * The switch is three-dimensional — action x platform x checker — because the
 * same action deserves different trust depending on the evidence behind it, and
 * because an operator's exposure differs per channel. */

export type AutomodAction =
  | "flag"
  | "hide_local"
  | "hide"
  | "delete"
  | "timeout"
  | "ban";

export type AutomodChecker = "rules" | "history" | "model";

/** One switch, plus whether the platform can do it at all.
 *
 *  `available` is derived server-side from the platform capability matrix and
 *  is not a stored setting. An unavailable cell renders inert with its reason
 *  rather than as an unticked box: a switch that silently does nothing is worse
 *  than no switch, because the operator believes the channel is protected. */
export interface AutomodCell {
  platform: string;
  action: AutomodAction;
  checker: AutomodChecker;
  auto: boolean;
  available: boolean;
  reason?: string;
}

export interface AutomodRule {
  id: number;
  name: string;
  enabled: boolean;
  pattern: string;
  action: AutomodAction;
  timeoutSeconds?: number;
}

/** Bounds for the per-author sequence detectors — the only checker that can see
 *  rate and repetition, which are properties of a sequence rather than a
 *  message. */
export interface AutomodHistory {
  windowSeconds: number;
  maxMessages: number;
  maxRepeats: number;
  maxLinks: number;
  maxMentionsPerMessage: number;
  minLengthForCaps: number;
  maxCapsRatio: number;
  action: AutomodAction;
  timeoutSeconds: number;
  retainPerAuthor: number;
  idleEvictionSeconds: number;
}

export interface AutomodModel {
  enabled: boolean;
  endpoint: string;
  model: string;
  /** The key itself is never returned. Set it through its own endpoint. */
  hasApiKey: boolean;
  timeoutSeconds: number;
  maxCallsPerHour: number;
  action: AutomodAction;
  timeoutForBan?: number;
  minConfidence: number;
  instruction: string;
}

export interface AutomodSettings {
  enabled: boolean;
  platformEnabled?: Record<string, boolean>;
  /** Cells switched on, keyed "platform/action/checker". Absent means off. */
  on?: Record<string, boolean>;
  rules?: AutomodRule[];
  history: AutomodHistory;
  model: AutomodModel;
}

/** What the operator sees about model spend and health. */
export interface AutomodModelStats {
  callsThisHour: number;
  ceiling: number;
  failures: number;
  lastError?: string;
  lastCallAt?: string;
}

/** The server-rendered matrix. Rows, columns and availability all come from the
 *  server so the UI never keeps a second copy of the vocabulary that could
 *  drift out of step with what the engine actually understands. */
export interface AutomodMatrixView {
  enabled: boolean;
  platformEnabled?: Record<string, boolean>;
  cells: AutomodCell[];
  summary: Record<string, number>;
  actions: AutomodAction[];
  checkers: AutomodChecker[];
  platforms: string[];
}

// ------------------------------------------------- lifecycle webhooks

export type HookTrigger =
  | "ingest.published"
  | "ingest.disconnected"
  | "destination.up"
  | "destination.down";

/** A stored lifecycle webhook. `url` is always masked and `secret` is never
 *  present: the plaintext key is returned once, by the create call, and cannot
 *  be read back. */
export interface Hook {
  id: number;
  name: string;
  enabled: boolean;
  url: string;
  hasSecret: boolean;
  triggers: HookTrigger[];
  timeoutSeconds: number;
  maxAttempts: number;
  createdAt: string;
  updatedAt: string;
}

export interface HookMeta {
  triggers: HookTrigger[];
  specVersion: string;
  headers: Record<string, string>;
  bounds: Record<string, number>;
  stats: {
    queued: number;
    dropped: number;
    sent: number;
    failed: number;
    retries: number;
    endpoints: number;
    lastSent?: string;
    lastError?: string;
  };
}

export interface HookDelivery {
  hookId: number;
  trigger: HookTrigger | "test";
  sequence: number;
  id: string;
  at: string;
  attempts: number;
  status?: number;
  durationMs: number;
  error?: string;
  response?: string;
}

/** What the test button gets back. It carries the exact body and signature that
 *  were sent, because the operator is checking their own verification code
 *  against real bytes rather than against the documentation. */
export interface HookTestResult {
  status: number;
  durationMs: number;
  response?: string;
  body: string;
  signature: string;
}

export interface HookCreated {
  id: number;
  hook: Hook;
  secret: string;
  secretNote: string;
}
