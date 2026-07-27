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

/** One choice in the encoder list. `available` is whether this FFmpeg build
 *  registers it; offering one it lacks costs a crash-looping stream to find. */
export interface EncoderInfo {
  name: string;
  codec: string;
  hardware: boolean;
  available: boolean;
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

export interface Settings {
  ingest: {
    mode: "srt" | "rtmp";
    srt: { port: number; passphrase: string; latencyMs: number };
    rtmp: { port: number; app: string; streamKey: string };
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
}

export interface SystemInfo {
  version: string;
  ffmpeg: FFmpegTools;
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
