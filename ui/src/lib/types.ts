/** Types mirroring the Go API. Kept hand-written and small rather than
 *  generated, so the shapes the UI actually consumes stay obvious. */

/** Mirrors routing.MaxTracks. The server also reports this as `maxTracks` in
 *  its capabilities response — prefer that where it is to hand, and treat this
 *  as the fallback for code that renders before capabilities have loaded. */
export const MAX_TRACKS = 32;

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
  /** What the server knows about this destination's current platform broadcast.
   *  Always present in a server response and empty for every destination on a
   *  platform whose broadcast is a side effect of bytes arriving rather than an
   *  object with a state machine — which today is everything except YouTube.
   *
   *  READ-ONLY. The server writes it through one narrow path of its own and
   *  ignores it on a destination save, so sending it back changes nothing. */
  lifecycle?: BroadcastControl;
  kind: DestKind;
  platform: Platform;
  accountId?: number | null;
  url: string;
  streamKey: string;
  /** The platform's secondary ingest, stored when the broadcast was created.
   *  Empty when the platform offered none. */
  backupUrl?: string;
  backupStreamKey?: string;
  /** Why this destination's stored stream key could not be read on this
   *  machine — a key file that was not restored alongside the database, or a
   *  database moved to a different host. Absent for every destination on a
   *  healthy install.
   *
   *  When it is present the server has already refused to run this
   *  destination: `enabled` reads false and `streamKey` is empty no matter
   *  what the row holds, because publishing with a key nobody can read is
   *  worse than not publishing. The text is the operator's instruction — type
   *  the key in again and switch it back on — and it is also carried on the
   *  status card's `warnings`, which is where the dashboard shows it. */
  keyUnreadable?: string;
  /** The operator's intent: publish a redundant feed for this destination.
   *  Sits beside the endpoint it gates rather than under `facebook`, because
   *  neither the engine nor the endpoint is platform-specific. Intent without
   *  an endpoint is the normal state between switching this on and the next
   *  broadcast being created; the card reports it and starts nothing. */
  backupIngestWanted?: boolean;
  enabled: boolean;
  audioBitrate: number;
  profile: RoutingProfile;
  /** Opt into Twitch Enhanced Broadcasting — what Amazon's IVS calls Multitrack
   *  Video: a negotiation at go-live that answers with an ingest endpoint, a
   *  minted stream key, and the audio tracks Twitch will accept.
   *
   *  Absent or false on nearly every destination, and that stays the common
   *  case rather than being a gap. Twitch refuses any client without a
   *  supported GPU, and polyemesis is installed on the operator's own
   *  server — a rented VPS has none. A negotiation that does not succeed is
   *  not a fault: the destination falls back to the ordinary ingest and says
   *  so once. Opt-in only because a network round trip at go-live should be
   *  something the operator asked for. */
  multitrack?: boolean;
  /** The SECOND audio mix — the VOD track, separate from the live one. Same
   *  shape as `profile`, because it is the same kind of thing.
   *
   *  Absent or null for every destination that has not opted in, which is
   *  nearly all of them, and null produces exactly the filter graph the
   *  destination produced before this field existed. Null rather than an empty
   *  profile: "no second mix" and "a second mix that happens to be the zero
   *  profile" are different, and the zero profile is not valid anyway.
   *
   *  On Twitch this needs `multitrack`. The ordinary Twitch RTMP ingest takes
   *  one audio track; Enhanced Broadcasting is the only published path that
   *  takes two. Nothing enforces that pairing — the engine reports it. */
  vodProfile?: RoutingProfile | null;
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
  /** The ceiling, in kbps. 0 derives it from videoBitrate, which is CBR and is
   *  what every rendition did before this field existed. Set it above the
   *  target to allow burst up to a platform's published maximum; it may not be
   *  set below the target, which is a contradiction rather than a preference. */
  maxrateKbps: number;
  /** The rate controller's window, in kbps. 0 derives it as twice the ceiling.
   *  Smaller windows correct faster and pump more visibly on a scene cut. */
  bufsizeKbps: number;
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

/** What happened to ONE destination in a bulk start or stop.
 *
 *  Mirrors api.bulkOutcome in internal/api/destinations_bulk.go.
 *
 *  - `started` / `stopped`  the intent was written, the pipeline reconciled and
 *                           the process state read back.
 *  - `warned`               it happened and something about it was NOT observed.
 *                           Today only the unreaped stop (#209): SIGKILL issued,
 *                           nobody waited, a child that may still be publishing.
 *  - `failed`               refused, with `message` saying why.
 *  - `skipped`              never attempted, so this destination is exactly as
 *                           it was. Reached when the request is cancelled part
 *                           way through a paced start.
 *
 *  Five words rather than four because "it happened but not cleanly" and "it did
 *  not happen at all" are different things to an operator, and both differ from
 *  a failure. */
export type BulkDestOutcome = "started" | "stopped" | "warned" | "failed" | "skipped";

/** One destination's row of a POST /destinations/start-all or /stop-all answer.
 *
 *  A BULK RESULT IS REPORTED PER DESTINATION, NEVER AS ONE BOOLEAN — the same
 *  doctrine the metadata composer states for a push. Eight destinations of which
 *  two refuse must not read as "failed", so this lives here beside MetaField
 *  rather than inside the page: it is an API contract, not a component detail. */
export interface BulkDestResult {
  id: number;
  name: string;
  platform: string;
  outcome: BulkDestOutcome;
  /** The supervisor's word for the process afterwards. Absent when the engine
   *  carries no process for this row. */
  state?: string;
  /** Why, on every outcome that is not a clean start or stop. */
  message?: string;
}

/** The whole answer to a bulk start or stop. */
export interface BulkDestReport {
  action: "start" | "stop";
  results: BulkDestResult[];
}

/** One font available to a text overlay, from GET /api/v1/fonts. */
export interface FontInfo {
  name: string;
  /** polyemesis rewrites the built-ins on every startup, so replacing one is
   *  undone by a restart. Warn rather than forbid. */
  builtIn: boolean;
}

/** A rendition plus its usage. `enabledDestinations` is the ref count the
 *  engine acts on: at zero there is no process and no CPU burnt. */
/** A platform's OWN published encoder guidance, served from
 *  GET /platforms/presets. Advisory: it seeds a form and annotates a choice,
 *  and never gates anything.
 *
 *  `source` and `checked` are not decoration. Once a bitrate is sitting in a
 *  form field an operator cannot tell a researched number from a guess, so the
 *  UI always shows where it came from and when it was last read. */
export interface VideoGuidance {
  width?: number;
  height?: number;
  fps?: number;
  kbpsMin?: number;
  kbpsMax?: number;
  gopSeconds?: number;
  note?: string;
  source: string;
  checked: string;
}

/** One entry of the server's platform catalogue. Only the fields the dialog
 *  reads are typed here — the UI keeps its own preset list for the picker and
 *  consults this for the researched data it must not duplicate. */
/** One ingest endpoint offered by a platform. The URL always carries an
 *  application path — that is the whole point of shipping a list rather than
 *  asking the operator to type one. */
export interface ServiceServer {
  name: string;
  url: string;
}

/** The platform's own published encoder ceiling. Zero means "no published
 *  figure", NOT "zero" — every reader has to check before comparing. */
export interface ServiceRecommended {
  keyintSeconds?: number;
  maxVideoKbps?: number;
  maxAudioKbps?: number;
  maxFps?: number;
  x264opts?: string;
}

/** A platform in the registry: where to publish, and what it will accept.
 *  Mirrors internal/services.Service. */
export interface ServiceInfo {
  id: string;
  name: string;
  /** True when the platform issues a different ingest host per channel, so
   *  `servers` is empty and the operator must supply the URL. Kick only. */
  perChannelIngest?: boolean;
  servers?: ServiceServer[];
  recommended: ServiceRecommended;
  videoCodecs?: string[];
  audioCodecs?: string[];
  streamKeyLink?: string;
  /** Why there is no server to pick, and what to paste instead. */
  note?: string;
}

export interface PlatformPresetInfo {
  id: string;
  name: string;
  video?: VideoGuidance;
}

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
  /** The redundant backup feed's live state, absent when there is no backup.
   *  Reported separately from `process` because a dead backup beside a healthy
   *  primary is the one state this must never hide. */
  backupProcess?: ProcessStatus | null;
  /** Why there is no backup, when one was asked for. */
  backupError?: string;
  /** What the Twitch Enhanced Broadcasting negotiation for this destination's
   *  CURRENT run decided, in one sentence. Absent for every destination that
   *  did not ask, which is nearly all of them.
   *
   *  Deliberately NOT in `warnings`, which the card renders in amber behind an
   *  alert triangle. Twitch grants this only to a client with a supported GPU
   *  and a rented server has none, so a fallback is what happens every time on
   *  most installs — rendering it as a warning would train the operator to read
   *  a perfectly normal broadcast as broken. */
  multitrackNote?: string;
  /** "negotiated", "advisory" or "refused" — so the card can tell "we asked and
   *  were turned down" from "we never asked". Absent when nothing asked. */
  multitrackVerdict?: string;
  /** Where Twitch's configuration departs from what this destination asked
   *  for. ADVISORY ONLY: these annotate a destination that IS publishing and
   *  must never be rendered as faults. */
  multitrackDivergences?: Divergence[] | null;
  /** Why this destination's second (VOD) audio mix is not on the wire. Empty
   *  when there is no second mix, and empty when there is one and it is going
   *  out. Not a warning: the destination is publishing correctly, one track
   *  short of what was configured, and the fix is a toggle not a repair. */
  vodAudioDropped?: string;
}

/** One advisory note about a negotiated Enhanced Broadcasting configuration. */
export interface Divergence {
  field: string;
  detail: string;
}

/* ---------------------------------------------------------------- Facebook
 *  broadcast lifecycle: ending one, and what its ingest looks like.
 *
 *  THESE LIVE HERE AND NOT IN DestinationCard.tsx for the reason MetaField
 *  above does: a server response names these fields, so their spelling is an
 *  API contract rather than a component detail, and a component that owns a
 *  contract is one nothing else can be checked against.
 *
 *  Mirrors internal/oauth/facebook.go — BroadcastEnd at :831 and
 *  IngestStreamHealth at :941. Read that file before changing anything here;
 *  every awkward-looking choice below is copied from a decision made there for
 *  a stated reason.
 */

/** What Facebook said after being asked to end a live video.
 *
 *  Mirrors oauth.BroadcastEnd. `ended` is true ONLY when Facebook read the
 *  status back as VOD. A `false` with no error is an ordinary outcome and NOT
 *  a failure — the POST succeeded and Facebook has not settled the node yet —
 *  so a renderer must not report it as "the broadcast is still live". */
export interface BroadcastEnd {
  /** The status Facebook reported on the read-back. Absent means the read-back
   *  failed or carried none, which is NOT the same as "still live". */
  status?: string;
  ended: boolean;
  /** What was actually seen when `ended` is false. Same shape metadata pushes
   *  use, so a caller renders them the same way. */
  warnings?: string[] | null;
}

/** One ingest stream's health, as Facebook reports it.
 *
 *  Mirrors oauth.IngestStreamHealth.
 *
 *  `health` IS A BAG OF FACEBOOK'S OWN FIELD NAMES, NOT NAMED FIELDS, and that
 *  is deliberate all the way down from the Go side: the LiveVideo node
 *  reference that would settle the spellings 404s, so the evidence establishes
 *  that stream_health carries bitrates and frame rates without naming the keys.
 *  A `bitrateKbps?: number` here would be a guess at a spelling, and a wrong
 *  guess reads back as `undefined` on a HEALTHY stream — a health pane with a
 *  permanently blank "Bitrate" row, on a broadcast that is fine, is the same
 *  false-report failure as rendering 0.
 *
 *  So a measurement that is absent is an ABSENT KEY. Never a zero, and never a
 *  row this file declared in advance and then could not fill. */
export interface IngestStreamHealth {
  id: string;
  health?: Record<string, number> | null;
  /** stream_health fields that were not numbers, sorted. Recorded rather than
   *  dropped because a field polyemesis cannot read looks exactly like a field
   *  Facebook did not send, and one of those is a bug on this side. */
  unparsed?: string[] | null;
}

/** The stream-health read for one destination.
 *
 *  `supported` is separate from an empty `streams` because they are different
 *  answers: false is "this platform publishes no stream health at all", while
 *  true with nothing in it is "Facebook publishes it and currently has no
 *  ingest to describe" — a scheduled broadcast, an ended one, or a live one
 *  inside Facebook's own four-second timeout. Collapsing the two would make
 *  the pause between clicking Go Live and the first byte look like a refusal.
 *
 *  Same arrangement internal/api already answers viewer stats with: 200 and
 *  supported:false, because "we cannot ask" and "the account is gone" are
 *  different problems with different fixes. */
export interface StreamHealthView {
  supported: boolean;
  streams?: IngestStreamHealth[] | null;
}

/** The floor on how often stream health may be polled, in milliseconds.
 *
 *  FACEBOOK'S NUMBER, NOT AN ESTIMATE OF ONE. Meta's Broadcasting guide, read
 *  2026-08-16, verbatim:
 *
 *    "Stream health data refreshes every 2 seconds, so limit queries to no
 *     more than once every 2 seconds. A stream timeout will be detected and
 *     reported after 4 seconds of no data being received."
 *
 *  A published floor may be encoded; an unpublished one may not. Mirrors
 *  oauth.FacebookStreamHealthInterval, which carries the same quote. */
export const FACEBOOK_STREAM_HEALTH_INTERVAL_MS = 2000;

/** How long Facebook itself waits before reporting a stream as timed out —
 *  the second sentence of the quote above. It is here so that a pane deciding
 *  "how long may this stay empty before the encoder is gone" reads Facebook's
 *  four seconds instead of inventing a number. Mirrors
 *  oauth.FacebookStreamTimeout. */
export const FACEBOOK_STREAM_TIMEOUT_MS = 4000;

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
  /** How completely the listener bound, when there is one to describe.
   *
   *  `running` and `tokenEnforced` are both booleans over a listener that can
   *  be HALF up: a wildcard SRT listener binds one socket per address family
   *  and deliberately survives one of them failing, so a source can be running,
   *  token-enforced, and unreachable for every encoder on the family that did
   *  not bind. `detail` is always set when the state is degraded — a bare
   *  "degraded" tells an operator nothing they can act on.
   *
   *  `degraded` means a family this HOST HAS was refused anyway — the port held
   *  by another process, a permission denied — and not merely that some
   *  requested address did not bind. An IPv4-only container cannot bind `[::]`
   *  and never will, so reporting that as degraded put a permanent orange badge
   *  on a perfectly healthy install, which teaches an operator to ignore the
   *  badge and costs them the one time it means something. The server draws
   *  that distinction from the errno; see engine.listenerHealthFor. The log
   *  line still records BOTH, because whoever is working out why an encoder
   *  will not connect needs the IPv4-only case too. */
  listenerHealth?: {
    state: "ok" | "degraded";
    detail?: string;
  };
  /** The server's sentence about an upload this source pulls from that nothing
   *  ever inspected, absent when there is nothing to say.
   *
   *  A pull source whose `file://` URL names a stored upload hands that path to
   *  the ENGINE's FFmpeg, which carries neither the format allowlist nor the
   *  probe the upload handler runs. Saving one is refused; a source ALREADY
   *  saved that way keeps running and says this instead. #255 argues the
   *  choice: an unverified verdict is a fact about the server (no ffprobe, a
   *  cut-short inspection) and never about the file, so refusing the ingest
   *  over it would take a programme off air for a toolchain problem.
   *
   *  Computed by the server rather than derived here, so a monitoring script
   *  reading GET /sources sees it too — the case #201 named is automation that
   *  configures a pull source from a listing and never looks at a card. */
  pullUploadUnchecked?: string;
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
  /** The hardware Twitch Enhanced Broadcasting is negotiated with. Optional for
   *  the same reason, and absent on nearly every install. */
  multitrack?: MultitrackSettings;
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

/** The GPU inventory Twitch Enhanced Broadcasting is negotiated with.
 *
 *  DECLARED, NOT DETECTED, and that is the design rather than a gap. Twitch
 *  validates this inventory and refuses by name — a vendor ID of zero, a vendor
 *  it does not recognise, an out-of-date driver — and polyemesis can measure
 *  exactly one of these six fields on one platform (the PCI vendor ID, from
 *  /sys/class/drm on Linux). Sending that one with zeros in the rest would be a
 *  description of a machine that does not exist.
 *
 *  Empty is the default and is not a fault: with nothing declared, no
 *  negotiation is attempted at all and every destination that opted in
 *  publishes to the ordinary Twitch ingest and says so once.
 *
 *  The vendor ID does not have to be guessed. The hardware panel already
 *  reports the PCI vendor ID of every render node found on this machine, and
 *  the NVIDIA driver version where it could be read. */
export interface MultitrackSettings {
  gpus?: MultitrackGpu[];
}

export interface MultitrackGpu {
  /** The adapter as its vendor names it, e.g. "NVIDIA GeForce RTX 4070".
   *  Required: it is what an operator reads back to check they filled in the
   *  right card. */
  model: string;
  /** PCI vendor ID as a DECIMAL integer — 4318 NVIDIA, 4098 AMD, 32902 Intel.
   *  Zero is refused when saving, because Twitch refuses it by name and a
   *  refusal three weeks later at go-live is not attached to the mistake. */
  vendorId: number;
  /** PCI device ID, decimal. Optional: an operator who cannot find it is
   *  better off sending nothing than inventing a number. */
  deviceId?: number;
  /** In BYTES, which is the unit the wire format uses. Optional. */
  dedicatedVideoMemory?: number;
  sharedSystemMemory?: number;
  /** The vendor's own driver version string. Optional — Twitch refuses an
   *  out-of-date driver naming the version to upgrade to, so an empty one is a
   *  refusal it cannot explain, but a wrong one invented to fill the box is
   *  worse than a missing one. */
  driverVersion?: string;
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
  /** A fact about the item that does NOT stop it going to air, so it is
   *  separate from `detail` and never accompanied by a non-"ready" state.
   *  Today it means one thing: the re-verify job refused this upload after a
   *  derivative had already been made from it. The copy on air keeps playing
   *  -- it was transcoded from those bytes and is intact -- and the operator
   *  is told so they can replace the file when it suits them. */
  warning?: string;
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
/** What a token is allowed to do.
 *
 *  `read` reaches GET and HEAD plus a short list of POSTs that compute an
 *  answer and write nothing; everything else is refused with a 403. `admin` is
 *  everything the signed-in operator can do, which is what every token was
 *  before scopes existed — tokens created before the upgrade are all `admin`,
 *  because narrowing a credential a running script is holding would break it
 *  without anyone being told.
 */
export type TokenScope = "read" | "admin";

export interface ApiToken {
  id: number;
  name: string;
  prefix: string;
  scope: TokenScope;
  createdAt: string;
  lastUsedAt: string;
}

/** What GET /version and POST /version/check return.
 *
 *  `latest`, `releaseUrl` and `checkedAt` stay empty until a check has actually
 *  run: the server never contacts GitHub on its own, which is deliberate for
 *  self-hosted software and is a property worth not breaking from this side. */
export interface VersionInfo {
  version: string;
  latest?: string;
  releaseUrl?: string;
  updateAvailable: boolean;
  /** False when either side is not a semantic version -- a dev build or a
   *  commit hash. The tag found is still reported, because "there is a v1.4.0,
   *  work out whether you have it" beats saying nothing. */
  comparable: boolean;
  checkedAt?: string;
  checkFailed?: boolean;
  /** What a restart would interrupt, surveyed fresh on every call. */
  onAir: OnAir;
  /** The sentence to show, empty when nothing is at stake. The server owns the
   *  wording so a browser and a terminal cannot disagree about a refusal. */
  onAirSummary?: string;
}

export interface OnAir {
  publishers: number;
  destinations: number;
  recording: boolean;
  names?: string[] | null;
}

/** What GET /upgrade/plan returns: what could be done about the available
 *  release, on this box, right now.
 *
 *  Its own request rather than a field on VersionInfo, and that is not a
 *  layering preference. Building the plan probes whether the install directory
 *  is writable by CREATING A FILE IN IT, which is the only check that survives
 *  a read-only mount -- and the update banner reads /version on every page
 *  load. Ask for this when an operator has actually asked to act. */
export interface UpgradePlan {
  /** How this install was made. The empty string is a server that could not
   *  tell, which is a refusal rather than a default. */
  method: "docker" | "systemd" | "manual" | "";
  /** Whether the server can perform the upgrade itself. False is the ORDINARY
   *  answer on a stock install: the systemd unit runs with
   *  ProtectSystem=strict, so /usr/local/bin is read-only to the service and
   *  the honest move is to show `command`. Do not render that as an error. */
  automatic: boolean;
  /** What the operator should run when `automatic` is false. Shown verbatim. */
  command?: string;
  binaryPath?: string;
  /** A previous binary is staged and could be restored. */
  rollbackAvailable: boolean;
  /** Why an upgrade is refused for a reason that is not about being on air --
   *  an unwritable directory, an install nothing could identify. */
  reason?: string;
  /** The release this plan is about: the tag the last check found. */
  version?: string;
  onAir: OnAir;
  onAirSummary?: string;
}

/** What POST /upgrade/stage and POST /upgrade/rollback return.
 *
 *  Neither ever reports that the upgrade has been APPLIED, because it has not:
 *  the binary on disk has changed and the running process has not. `command` is
 *  what makes it take effect, and the UI must say so rather than "updated". */
export interface UpgradeResult {
  staged?: boolean;
  rolledBack?: boolean;
  version?: string;
  restartRequired: boolean;
  command: string;
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

/** A point-in-time read of one connected channel's broadcast, from
 *  GET /api/v1/platforms/accounts/{id}/stats.
 *
 *  Mirrors oauth.LiveStats. Kept HERE rather than in the component that renders
 *  it, for the same reason MetaField is: the server names these fields, so the
 *  shape is an API contract rather than a component detail.
 *
 *  EVERY OPTIONAL FIELD BELOW IS OPTIONAL ON PURPOSE AND `viewerCount` IS THE
 *  ONE THAT MATTERS. Go declares it `*int` with `omitempty`, so a platform that
 *  declined to say drops the key entirely rather than sending a zero — see the
 *  comment on oauth.LiveStats.ViewerCount, which is the specification for this
 *  type. Typing it `number` here would compile, and would then let any caller
 *  write `{stats.viewerCount}` or `count || 0` and tell a streamer with an
 *  audience that nobody is watching. `number | undefined` is what forces the
 *  branch, and viewerReadout() in lib/viewerCount.ts is where that branch is
 *  taken once for the whole app. */
export interface LiveStats {
  live: boolean;
  /** ABSENT IS NOT ZERO. YouTube omits it when nobody is watching, when the
   *  owner has hidden the count, and once the broadcast has ended; Kick
   *  documents 0 as the streamer's opt-out value; Twitch sends no count at all
   *  for a channel that is not live. Render "not reported", never 0. */
  viewerCount?: number;
  title?: string;
  category?: string;
  language?: string;
  slug?: string;
  /** RFC 3339. Absent for an offline channel and for a stamp the server could
   *  not parse — Go makes it `*time.Time` because `omitempty` does nothing to a
   *  struct, and the zero time used to serialise as a confident 0001-01-01. */
  startedAt?: string;
  /** The endpoint the numbers came from, so a count that disagrees with the
   *  platform's own dashboard can be traced without a packet capture. */
  source?: string;
}

/** The stats envelope, as a discriminated union on `supported`.
 *
 *  A union rather than `{supported: boolean; reason?: string; stats?: LiveStats}`
 *  because the handler answers 200 in both cases and the two cases carry
 *  different fields: "polyemesis cannot ask this platform" is a capability
 *  answer with a reason to show, not an error and not an absent count. The union
 *  makes reaching for `stats` without checking `supported` a type error, which
 *  is the branch oauth.StatsFor's doc comment asks every caller to take. */
export type AccountStats =
  | {
      supported: false;
      /** Shown verbatim: the server writes the platform's name into it
       *  ("polyemesis does not read a viewer count from facebook"), and an
       *  operator deciding whether to wait for a number needs to know it is
       *  never coming. */
      reason: string;
    }
  | { supported: true; stats: LiveStats };

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

/** Whether the onboarding tour has already been offered to this operator.
 *
 *  Held on the server, against the user row, rather than in localStorage. The
 *  difference is a real one: an operator who sets this install up on a desktop
 *  and opens it later from a laptop has already seen the tour, and a per-browser
 *  flag would offer it to them again on every new browser, machine and private
 *  window. See internal/db/schema.sql. */
export interface TourState {
  completed: boolean;
  /** Unix seconds; 0 when the tour has never been finished or dismissed. */
  completedAt: number;
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

/** The prerequisites for a Let's Encrypt certificate, checked against this
 *  host. See internal/api/acme_preflight.go. */
export type AcmeCheckId = "name" | "dns" | "port80" | "email" | "issuance";

/** `unknown` is not a soft failure. It is the answer where this server cannot
 *  see far enough — whether the public internet reaches port 80, whether a
 *  record pointing off-box is NAT or a mistake — and it must not read as a
 *  fault, because the operator has no way to clear it from inside the box. */
export type AcmeCheckStatus = "pass" | "fail" | "unknown";

export interface AcmeCheck {
  id: AcmeCheckId;
  status: AcmeCheckStatus;
  /** English prose from the server, like `TlsStatus.certificateError`: it names
   *  config keys, systemd directives and paths, and is what an operator pastes
   *  into a search. The UI translates the label, not this. */
  detail: string;
}

export interface AcmePreflight {
  /** The name the checks ran against. */
  hostname: string;
  mode: TlsMode;
  /** Nothing the server can see would stop issuance. Not a promise that it
   *  will succeed — see AcmeCheckStatus. */
  ready: boolean;
  checks: AcmeCheck[];
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

/** A chat search result set.
 *
 *  Carries the same two honesty fields as ChatUserCard and for a sharper
 *  reason: search is the one place an operator can conclude something did NOT
 *  happen. "No results" here means "not in the scrollback we kept", never "never
 *  said", so `retentionNote` has to be rendered alongside an empty result and
 *  not only alongside a full one. */
export interface ChatSearchResult {
  query: string;
  /** Echoed back so the UI can label a narrowed result set. */
  platform?: ChatPlatform;
  messages: ChatMessage[];
  /** The limit was reached; older matches may exist beyond this page. */
  truncated: boolean;
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
  /** Forward the selected ingest tracks untouched -- no decode, no mix, no
   *  encoder -- so this destination carries the same bits your encoder sent.
   *
   *  SRT and file destinations only. It is called copy and not "passthrough"
   *  because passthrough already means a null rendition, i.e. video at the
   *  ingest's own resolution, everywhere else in this app.
   *
   *  Copy still SELECTS: which tracks go out, and which roles are excluded,
   *  both still apply. What it gives up is everything the mix does to the
   *  samples -- gain, normalization, loudness, ducking, delay, mono -- and a
   *  destination that asks for copy and for any of those is refused on save
   *  rather than silently ignoring one of them. */
  copy?: boolean;
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
  /** COPPA self-declaration. false is the real declaration "this is not for
   *  children", and is different from having said nothing.
   *
   *  Three states, and null is the one that carries: an update is decoded over
   *  the stored row, so `undefined` is omitted from the body and leaves
   *  whatever was already there. Going back to "not said" has to send an
   *  explicit null, which db.Compliance.MadeForKids reads as nil. */
  madeForKids?: boolean | null;
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
}

/** What the server knows about one destination's current platform broadcast.
 *  Mirrors db.BroadcastControl field for field.
 *
 *  Everything here is a RECORD OF WHAT THE PLATFORM SAID, never a belief about
 *  what polyemesis asked for — which is what makes it worth showing an operator
 *  whose broadcast will not start. */
export interface BroadcastControl {
  /** Which broadcast the rest of this describes. */
  broadcastId?: string;
  /** The platform's own word for where the broadcast is — for YouTube:
   *  created, ready, testing, testStarting, live, liveStarting, complete,
   *  revoked. Passed through unmapped so it can be compared against what the
   *  platform's own console shows. */
  phase?: string;
  /** How many consecutive sweeps have failed. */
  attempts?: number;
  /** What went wrong, in the operator's words, or absent when nothing has.
   *
   *  A fault here NEVER means the stream stopped: the server does not stop a
   *  stream because a broadcast transition failed. It means the bytes are going
   *  out and the platform has not put them in front of an audience. */
  fault?: string;
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
  /** What ffprobe found when the file was accepted. Absent whenever the file was
   *  not inspected — which is never "it was inspected and found to be nothing" —
   *  so render nothing rather than zeroes. */
  media?: MediaInfo;
  /** Whether these bytes were inspected and accepted as media.
   *
   *  ALWAYS PRESENT, and that is the point of it. An upload is in one of three
   *  states: inspected and accepted, refused (in which case it is not here), and
   *  STORED WITHOUT BEING INSPECTED. The third is reachable on demand by a remote
   *  client — the server's probe runs under the request's context, so dropping
   *  the connection after the body has landed cancels it, and a cancelled probe
   *  is not a verdict, so the bytes are kept. Keeping them is right; leaving the
   *  result indistinguishable was not. Before this field the only trace was the
   *  ABSENCE of `media`, which is also what every upload stored before probing
   *  looks like. */
  verified: boolean;
  /** Which state this upload is in — the field to branch on.
   *
   *  `verified` is still true only for "inspected and accepted", but false now
   *  covers three different things with three different remedies, and telling an
   *  operator what to DO requires knowing which:
   *
   *  - `unverified` — this server produced no verdict about the bytes (the check
   *    was cut short, or could not run). A fact about the SERVER. Upload it again.
   *  - `refused` — the bytes WERE inspected and are not media this server takes.
   *    A fact about the FILE, and permanent: re-sending it changes nothing.
   *  - `unrecorded` — nothing was ever written about this file, which is every
   *    upload stored before verdicts existed. Still usable; refusing these would
   *    strand media an operator has had for a year.
   *
   *  ALWAYS PRESENT. `unrecorded` used to be inferred client-side from an empty
   *  `unverifiedReason`, which was never the same question and stopped being a
   *  usable proxy once a recorded state could carry no reason of its own. */
  outcome: MediaVerdict;
  /** Why this file is not verified, in the operator's words. Set for both
   *  `unverified` and `refused` — `outcome` is what says which kind of reason it
   *  is. Empty only for `unrecorded`. */
  unverifiedReason?: string;
}

/** The four states of {@link MediaFile.outcome}. Three of them are recorded
 *  beside the file; `unrecorded` is the absence of a record, which is a distinct
 *  answer from every recorded one and must stay that way. */
export type MediaVerdict = "verified" | "unverified" | "refused" | "unrecorded";

/** MediaInfo is a stored upload's probe result, as the Library shows it. */
export interface MediaInfo {
  durationSeconds: number;
  videoCodec: string;
  width: number;
  height: number;
  frameRate: number;
  /** The count routing cares about: selecting track 3 of a file that carries
   *  one is silence on air, and the Library is where to notice beforehand. */
  audioTracks: number;
  audioCodec: string;
  audioChannels: number;
  audioLayout: string;
  probedAt: string;
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
  | "destination.down"
  /** A platform refused to move a broadcast's state and somebody has to act.
   *  NOT a destination.down: the stream is fine and is still being delivered —
   *  what failed is the platform's idea of the broadcast. A script that mirrors
   *  "what are we live to" must not tear anything down when it hears this. */
  | "broadcast.fault";

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
