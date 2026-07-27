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

export interface RoutingProfile {
  mode: RoutingMode;
  tracks: TrackSel[];
  matrix: MatrixCell[] | null;
  normalize: NormalizeMode;
  sampleRate: number;
}

export interface RoutingResult {
  filterComplex: string;
  outLabel: string;
  summary: string;
  tracks: number[];
  normalization: NormalizeMode;
  warnings: string[] | null;
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
}

export type DestKind = "rtmp" | "srt" | "file";
export type Platform = "custom" | "youtube" | "twitch" | "kick";

export interface Destination {
  id: number;
  name: string;
  kind: DestKind;
  platform: Platform;
  accountId?: number | null;
  url: string;
  streamKey: string;
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
  note: string;
  createdAt: string;
  updatedAt: string;
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

export interface Settings {
  ingest: {
    mode: IngestMode;
    srt: { port: number; passphrase: string; latencyMs: number };
    rtmp: { port: number; app: string; streamKey: string };
    /** Optional so a client that predates pull can still PUT settings. */
    pull?: PullSettings;
  };
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

export interface Preset {
  id: string;
  name: string;
  description: string;
  needsMusicTrack: boolean;
  needsMicTrack: boolean;
  needsSurroundTrack: boolean;
}

export interface PresetOpts {
  musicTrack: number;
  micTrack: number;
  surroundTrack: number;
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
  createdAt: string;
  updatedAt: string;
}

export interface SetupGuide {
  platform: Platform;
  name: string;
  consoleUrl: string;
  redirectPath: string;
  steps: string[];
  scopes: string[] | null;
  supported: boolean;
  note?: string;
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
  | "recordings";

export interface WsEvent {
  type: EventType;
  time: string;
  data: unknown;
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
