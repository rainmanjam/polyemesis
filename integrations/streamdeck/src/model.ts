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
