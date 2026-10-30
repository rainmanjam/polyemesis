// The subset of the polyemesis API this plugin reads. Field names are the JSON
// names the Go server emits (internal/engine/status.go, selector.go,
// destinations_stop_impact.go, handlers.go). Anything the plugin does not read
// is left out on purpose: a model that mirrors the whole payload drifts silently.

export type ProcessState = "stopped" | "starting" | "running" | "reconnecting" | "failed";

export interface ProcessStatus {
  state: ProcessState;
  lastError?: string;
  nextRetryIn?: number;
}

/** The classified causes (internal/failreason). The same codes reach MQTT,
 *  webhooks and alerts. */
export type FailureCause =
  | "key_rejected"
  | "platform_not_ready"
  | "dns"
  | "connection_refused"
  | "timeout"
  | "tls"
  | "codec_rejected"
  | "upload_too_slow"
  | "unknown";

/** DestStatus.reason: only the fields a key face reads. */
export interface FailureReason {
  cause: FailureCause;
  /** The FFmpeg line, scrubbed by the server. Shown only for "unknown". */
  line?: string;
  /** Retrying has stopped after repeated quick rejections. */
  held?: boolean;
  /** The connected account reports a different stream key. */
  keyChanged?: boolean;
}

export interface DestStatus {
  id: number;
  name: string;
  platform: string;
  enabled: boolean;
  /** The programme (source) this destination carries. Absent on legacy rows. */
  sourceId?: number;
  process?: ProcessStatus | null;
  backupProcess?: ProcessStatus | null;
  backupError?: string;
  error?: string;
  /** Process is running and has moved media, but its output has stopped advancing. */
  stalled?: boolean;
  stopWarning?: string;
  /** A reconcile is in flight; the process may be briefly absent. */
  transitioning?: boolean;
  /** Why it is failing, or delivering too slowly. Absent while nothing is wrong. */
  reason?: FailureReason | null;
  /** The configured broadcast delay in seconds (#11), absent when none. */
  delaySeconds?: number;
  /** The live delay buffer. phase "draining" is a STOPPED destination still on
   *  the air with its buffered tail. */
  delay?: { seconds: number; phase: "buffering" | "on-air" | "draining"; drainRemainingSeconds?: number } | null;
}

export interface HoldStatus {
  code: string;
  reason: string;
}

export interface StatusPayload {
  destinations?: DestStatus[];
  destinationHold?: HoldStatus | null;
  failover?: FailoverStatus | null;
}

export type SourceKind = "" | "primary" | "backup" | "playlist" | "slate";

export interface FailoverStatus {
  active: SourceKind;
  reason?: string;
  error?: string;
}

export interface StopImpact {
  destinations: number;
  youtubeBroadcasts: number;
}

/** GET /api/v1/recording. Install-wide: one switch for every programme. */
export type RecordingStateWord = "off" | "recording" | "not_running" | "halted_disk";

export interface RecordingState {
  enabled: boolean;
  state: RecordingStateWord;
  halted: boolean;
  reason?: string;
  installWide: boolean;
}

/** One entry of GET /api/v1/routing/presets. Only what a key needs. */
export interface PresetSummary {
  id: string;
  name: string;
}

export interface ConnectionInfo {
  name: string;
  prefix: string;
  scope: "read" | "admin";
  adminScope: boolean;
}

export interface LevelsFrame {
  peak: number[][];
  rms: number[][];
}

/** The feed state the server computes for GET /levels. See internal/api/levels_feed.go. */
export type Feed = "none" | "live" | "stale";

export interface LevelsReading {
  levels: LevelsFrame | null;
  feed: Feed;
}

/** One track of a destination's stored selection (profile.tracks[] in internal/routing/profile.go). */
export interface TrackLevel {
  /** The 0-based ingest track index. */
  track: number;
  enabled: boolean;
  /** Linear: 1.0 is unity. The server caps it at routing.MaxGain. */
  gain: number;
  muted: boolean;
}

/**
 * What the plugin keeps of one GET /destinations row: enough to show and change a track's
 * level. Publish credentials are in that answer for an admin token; none of them is kept.
 */
export interface DestinationLevels {
  id: number;
  name: string;
  /** The programme the destination carries. Absent on legacy rows. */
  sourceId?: number;
  /** A matrix mix takes its gain per cell, so the server refuses a track gain (mute still applies). */
  matrix: boolean;
  /** The audio is passed through unchanged, so no level can apply: the server answers 409. */
  copyAudio: boolean;
  tracks: TrackLevel[];
}

/** How a track level reached the output: on the running process's command channel, or by a (re)start. */
export type LevelApplied = "live" | "restart";

/** The answer of POST /destinations/{id}/tracks/{track}/gain. */
export interface TrackLevelResult {
  applied: LevelApplied;
  /** The server's own words for why, sent with a restart and with some live answers. */
  reason?: string;
  /** The stored destination after the change, when the answer carries one the plugin can read. */
  destination?: DestinationLevels;
}
