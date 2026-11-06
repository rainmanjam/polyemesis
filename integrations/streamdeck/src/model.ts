// The subset of the polyemesis API this plugin reads. Field names are the JSON
// names the Go server emits (internal/engine/status.go, selector.go,
// destinations_stop_impact.go, handlers.go). Anything the plugin does not read
// is left out on purpose: a model that mirrors the whole payload drifts silently.

/** The wire words below are checked against the server's own list: see
 *  test/wireContract.test.ts and contract/wire.json, which Go generates. */
export const PROCESS_STATES = ["stopped", "starting", "running", "reconnecting", "failed"] as const;
export type ProcessState = (typeof PROCESS_STATES)[number];

export interface ProcessStatus {
  state: ProcessState;
  lastError?: string;
  nextRetryIn?: number;
}

/** The classified causes (internal/failreason). The same codes reach MQTT,
 *  webhooks and alerts. */
export const FAILURE_CAUSES = [
  "key_rejected",
  "platform_not_ready",
  "dns",
  "connection_refused",
  "timeout",
  "tls",
  "codec_rejected",
  "upload_too_slow",
  "unknown",
] as const;
export type FailureCause = (typeof FAILURE_CAUSES)[number];

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

/** DestStatus.delay: the live delay buffer of a delayed destination. */
export interface DelayStatus {
  /** The CONFIGURED delay. */
  seconds: number;
  phase: "buffering" | "on-air" | "draining";
  drainRemainingSeconds?: number;
  /** The delay actually running: shorter than `seconds` after a dump, until the destination next starts.
   *  The server always sends it; an older server does not, and then it is `seconds`. */
  effectiveSeconds?: number;
  /** What the dumps since the last start have dropped. */
  dumpedSeconds?: number;
  /** A dump is waiting for its keyframe. */
  dumping?: boolean;
  /** A dump would be accepted now: on the air, with something since the last dump point. */
  dumpable?: boolean;
}

/** The answer of POST /destinations/{id}/delay/dump: only what a key reads. */
export interface DumpResult {
  id: number;
  name: string;
  dumpedSeconds: number;
}

/** One row of POST /destinations/delay/dump-all. */
export interface DumpAllRow {
  id: number;
  name: string;
  outcome: "dumped" | "skipped" | "failed";
  message?: string;
  dumpedSeconds?: number;
}

export interface DestStatus {
  id: number;
  name: string;
  platform: string;
  enabled: boolean;
  /** The programme (source) this destination carries. Absent on legacy rows. */
  sourceId?: number;
  /** The YouTube vertical half this row is paired with (#7). Absent when it has none. */
  youtubeVertical?: { verticalId: number; pairingConfirmed: boolean } | null;
  /** Set on a vertical row: the horizontal destination it belongs to (#7). */
  verticalHalfOf?: number | null;
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
  delay?: DelayStatus | null;
  /** Respawns in the last 15 minutes, present only when more than five: the
   *  server's restart-storm verdict, the PolyemesisDestinationRestartStorm threshold. */
  restartStorm?: number;
  /** Frames dropped over the last few seconds, present only when the server
   *  judges it at least one percent of the frames output. */
  droppingFrames?: number;
  /** The connected account's health, present only when it is not ok. The state
   *  word only: the server keeps the refresh error off this read-scope payload. */
  accountHealth?: AccountHealthState;
  /** The platform's own verdict on the stream, present only while it is poor. */
  platformHealth?: PlatformHealth | null;
}

/** "ok" is absent: the field is only sent when the account is not healthy. */
export const ACCOUNT_HEALTH_STATES = ["expiring", "refreshFailing", "expired"] as const;
export type AccountHealthState = (typeof ACCOUNT_HEALTH_STATES)[number];

/** DestStatus.platformHealth (internal/engine/destsignals.go). */
export interface PlatformHealth {
  /** The platform's id, e.g. "youtube". */
  platform: string;
  /** The platform's word, verbatim: YouTube's good, ok, bad or noData. */
  status: string;
  /** The platform's codes for its error-severity issues. */
  issues?: string[];
}

export interface HoldStatus {
  code: string;
  reason: string;
}

export interface StatusPayload {
  destinations?: DestStatus[];
  destinationHold?: HoldStatus | null;
  /** The failover of the programme this status was scoped to: source.id's. */
  failover?: FailoverStatus | null;
  /** The programme the scoped parts of this answer (failover) describe. The
   *  destination list is install-wide, so this is what places a failover. */
  source?: { id: number } | null;
}

export const SOURCE_KINDS = ["", "primary", "backup", "playlist", "slate"] as const;
export type SourceKind = (typeof SOURCE_KINDS)[number];

export interface FailoverStatus {
  active: SourceKind;
  reason?: string;
  error?: string;
  /** Set while the primary's link is judged degraded (#6): the measured
   *  explanation, e.g. "bitrate 640 kbps below 800 kbps for 5s". Present even
   *  when the primary is still on air because there was nowhere to switch to. */
  degraded?: string;
}

export interface StopImpact {
  destinations: number;
  youtubeBroadcasts: number;
}

/** GET /api/v1/recording. Install-wide: one switch for every programme. */
export const RECORDING_STATE_WORDS = ["off", "recording", "not_running", "halted_disk"] as const;
export type RecordingStateWord = (typeof RECORDING_STATE_WORDS)[number];

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

/**
 * One destination's row of POST /routing/presets/{preset}/apply. `saved`: the preset is stored on it, and
 * `applied` says how it reached the running output ("live" or "restart"; absent when it was not running).
 * `skipped`: not this preset's to change (audio copy, or deleted meanwhile). `failed`: any outcome this build
 * does not know, which is never counted as saved.
 */
export interface PresetApplyRow {
  id: number;
  name: string;
  outcome: "saved" | "skipped" | "failed";
  applied?: "live" | "restart";
  message?: string;
}

/** POST /routing/presets/{preset}/apply. reconcileFailed: stored, and the running pipeline was not updated. */
export interface PresetApplyResult {
  results: PresetApplyRow[];
  reconcileFailed: boolean;
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

/** The running chat poll (#14), as GET /polls answers `current`. Counts only: the server sends no voter. */
export interface PollSummary {
  id: number;
  question: string;
  /** "open" or "closing" while it runs. */
  state: string;
  total: number;
  endsAt: string;
}

/** What a poll key starts: the body of POST /polls. */
export interface PollSpec {
  question: string;
  options: string[];
  durationSeconds: number;
}

/**
 * The cross-platform audience (Wave 4 #4), as GET /analytics/live answers it, reduced to what a viewers key
 * shows. A count a platform did not report is ABSENT, never 0: `total` is the sum of what did report and is
 * absent when nothing did. The public player is a reading with platform "player".
 */
export interface ViewersLive {
  enabled: boolean;
  onAir: boolean;
  total?: number;
  /** A platform failed at the last tick: `total` is the rest of the audience, not all of it. */
  partial?: boolean;
  readings: { platform: string; viewers?: number }[];
}
