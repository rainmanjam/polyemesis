import type { DestStatus, FailoverStatus, FailureCause, HoldStatus, SourceKind } from "./model.js";
import type { StreamDeckKey } from "./keys.js";

// THE FACE NEVER LIES. Every key shows what the server last said, and when the
// server cannot say, the key says so. Nothing here infers a state the payload
// does not carry: a stalled encoder is not "live", a null process on an enabled
// destination is "starting" (a reconcile may be in flight, see #462), and a
// backup that is dead beside a healthy primary is shown, never hidden.

/**
 * The colour family a key is drawn in. Green means live and nothing else.
 * "halted" is the Record key's HALTED: DISK face, its own image so an unreachable
 * install (error) can never be mistaken for a full disk. "needsAdmin" is a write
 * key on a read token: its own lock face, not the generic warn tile. The last three are the
 * track mute key's: a muted track (a crossed speaker, so it is never read as "unreachable"), a
 * level that reached the output by a restart (the viewer saw a reconnect), and a control that
 * cannot apply to this destination.
 */
export type KeyTone =
  | "live"
  | "warn"
  | "error"
  | "idle"
  | "held"
  | "unbound"
  | "halted"
  | "needsAdmin"
  | "muted"
  | "restarted"
  | "unavailable";

export interface KeyFace {
  tone: KeyTone;
  label: StreamDeckKey;
  /** Placeholders for the label, e.g. the count in "Reconnecting ×{count}". */
  labelVars?: Record<string, string | number>;
  /** The server's classified cause, as a catalogue key. A translation of the
   *  server's code, not an inference made here. */
  reason?: StreamDeckKey;
  /** Placeholders for the reason, e.g. the platform in "{platform}: poor". */
  reasonVars?: Record<string, string | number>;
  /** Server words, passed through unchanged. Never invented here. */
  detail?: string;
  /** A second, independent problem worth a badge, e.g. a dead backup. */
  warning?: StreamDeckKey;
  /** Server words for the warning, when the warning is the server's own text. */
  warningDetail?: string;
  /** How many current issues the face does not show: the "+N" badge. */
  more?: number;
  /** Every current issue, most severe first. The face shows the first; the
   *  property inspector lists them all. Absent on keys that have none. */
  issues?: KeyIssue[];
}

/**
 * The issues a destination key can carry, most severe first.
 *
 * - held: the server has stopped retrying; only a Retry changes it.
 * - failed: a real failure, which no amount of waiting will fix. That is a
 *   process that gave up ("failed"), or a key the platform says has changed, in
 *   whatever state, because it names the one-click fix.
 * - account: the connected account has expired or keeps failing to refresh.
 * - cycling: a destination that is stalled, or reconnecting with a cause. It
 *   is retrying, so this is transient, and it ranks BELOW the account. Otherwise
 *   a restart storm on an expired account flips the key between red "Reconnect
 *   account" (while up) and amber "Reconnecting" (between runs) on every cycle.
 *   The account is the persistent problem, and probably the cause, so the face
 *   holds on it until it clears.
 * - accountExpiring: a warning about tomorrow, below anything happening now.
 * - The rest: a restart storm, dropped frames, the platform's verdict and the
 *   programme's source are an on-air stream going wrong. A slow upload is the
 *   mildest, because it IS on air.
 */
export const ISSUE_RANK = [
  "held",
  "failed",
  "account",
  "cycling",
  "accountExpiring",
  "restartStorm",
  "droppingFrames",
  "platform",
  "source",
  "uploadSlow",
] as const;
export type IssueKind = (typeof ISSUE_RANK)[number];

/** One current issue of a destination. */
export interface KeyIssue {
  kind: IssueKind;
  tone: KeyTone;
  /** What the issue is, as shown on the key's reason line. */
  key: StreamDeckKey;
  vars?: Record<string, string | number>;
  /** The state the issue is about, when the state itself is the issue (Failed, Held). */
  state?: StreamDeckKey;
  stateVars?: Record<string, string | number>;
  /** Server words, shown on the key and in the inspector. */
  detail?: string;
  /** Supporting facts for the inspector only: a count, the platform's codes. */
  note?: string;
}

/** Where the destination's programme is, from the same /status answer. */
export interface SourceContext {
  /** The programme the payload's failover describes (StatusPayload.source.id). */
  sourceId?: number;
  failover?: FailoverStatus | null;
}

/** The face of a destination key bound to one destination and, optionally, one programme. */
export function destinationFace(
  dest: DestStatus | undefined,
  boundSourceId: number | undefined,
  hold: HoldStatus | null | undefined,
  source?: SourceContext,
): KeyFace {
  if (!dest) {
    return { tone: "unbound", label: "streamdeck.state.missing" };
  }
  // A key bound to programme A must never show the state of a destination on
  // programme B. Showing it would look like a fact about A.
  if (boundSourceId !== undefined && dest.sourceId !== boundSourceId) {
    return { tone: "unbound", label: "streamdeck.state.otherProgramme" };
  }

  const state = stateFace(dest, hold);
  const issues = [...(state.issue ? [state.issue] : []), ...signalIssues(dest, source, state.stormOnFace)];
  issues.sort((a, b) => ISSUE_RANK.indexOf(a.kind) - ISSUE_RANK.indexOf(b.kind));

  // The face is the state, unless a worse issue than the state's own is open.
  // Then the label stays the true state word -- a red "Reconnect account" on a
  // destination that is on air still says LIVE -- and the issue takes the
  // reason line and the colour.
  const top = issues[0];
  let face: KeyFace = state.face;
  if (top && top !== state.issue) {
    face = {
      tone: top.tone,
      label: state.face.label,
      ...(state.face.labelVars ? { labelVars: state.face.labelVars } : {}),
      reason: top.key,
      ...(top.vars ? { reasonVars: top.vars } : {}),
      ...(top.detail ? { detail: top.detail } : {}),
    };
  }
  face = withWarning(face, warningFor(dest));
  if (issues.length > 0) face.issues = issues;
  if (issues.length > 1) face.more = issues.length - 1;
  return face;
}

/** The key's state, and the issue that state is, if it is one. */
interface StateFace {
  face: KeyFace;
  issue?: KeyIssue;
  /** The restart count is already on the face (a reconnecting storm). */
  stormOnFace?: boolean;
}

/** The issue a state face is: the state, its reason and the server's words. */
function asIssue(kind: IssueKind, face: KeyFace): KeyIssue {
  return {
    kind,
    tone: face.tone,
    key: face.reason ?? face.label,
    // The state word leads the inspector's line only when the state IS the
    // problem: "Failed: Key rejected", but plain "Upload slow" on a live key.
    ...(face.reason && kind !== "uploadSlow" ? { state: face.label } : {}),
    ...(face.reason && kind !== "uploadSlow" && face.labelVars ? { stateVars: face.labelVars } : {}),
    ...(!face.reason && face.labelVars ? { vars: face.labelVars } : {}),
    ...(face.reason && face.reasonVars ? { vars: face.reasonVars } : {}),
    ...(face.detail ? { detail: face.detail } : {}),
  };
}

/** What the destination's process says: the face this key has always had. */
function stateFace(dest: DestStatus, hold: HoldStatus | null | undefined): StateFace {
  const proc = dest.process ?? null;

  if (!proc) {
    if (!dest.enabled) return { face: { tone: "idle", label: "streamdeck.state.off" } };
    if (hold) {
      const face: KeyFace = { tone: "held", label: "streamdeck.state.held", detail: hold.reason };
      return { face, issue: asIssue("held", face) };
    }
    // Enabled with no process: the server is between states, not off.
    return { face: { tone: "warn", label: "streamdeck.state.starting" } };
  }

  // The server's classified cause (#20). A key known to this plugin replaces
  // the raw FFmpeg tail, which on 72 px is a clipped exit status; an unknown
  // cause, or a code newer than this plugin, keeps the server's own words.
  const why = reasonFor(dest);

  switch (proc.state) {
    case "running": {
      if (dest.stalled) {
        const face: KeyFace = { tone: "warn", label: "streamdeck.state.stalled" };
        return { face, issue: asIssue("cycling", face) };
      }
      // A slow upload does not exit, so it is the one cause shown on a running
      // destination. Live, in the warn colour: it IS on air, badly.
      if (why.reason === "streamdeck.reason.upload_too_slow") {
        const face: KeyFace = { tone: "warn", label: "streamdeck.state.live", reason: why.reason };
        return { face, issue: asIssue("uploadSlow", face) };
      }
      return { face: { tone: "live", label: "streamdeck.state.live" } };
    }
    case "starting":
      return { face: { tone: "warn", label: "streamdeck.state.starting" } };
    case "reconnecting": {
      // A restart storm is what reconnecting looks like when it keeps
      // happening: one issue, so the count goes on the label and the cause
      // stays on the line beneath, rather than the two being counted twice.
      const storm = stormCount(dest);
      const face: KeyFace = {
        tone: "warn",
        label: storm ? "streamdeck.issue.restartStorm" : "streamdeck.state.reconnecting",
        ...(storm ? { labelVars: { count: storm } } : {}),
        ...(why.known ? withoutKnown(why) : withDetail(why.detail ?? proc.lastError)),
      };
      // Retrying is transient ("cycling"), unless the platform says the key
      // changed: that is a real failure with a named fix, and outranks the account.
      const kind = why.reason === "streamdeck.reason.keyChanged" ? "failed" : "cycling";
      return { face, issue: asIssue(kind, face), stormOnFace: storm > 0 };
    }
    case "failed": {
      // Held is its own tile: the server has stopped retrying, and only a
      // Retry from the console will change what this key shows.
      if (dest.reason?.held) {
        const face: KeyFace = { tone: "held", label: "streamdeck.state.held", ...withoutKnown(why) };
        return { face, issue: asIssue("held", face) };
      }
      const face: KeyFace = {
        tone: "error",
        label: "streamdeck.state.failed",
        ...(why.known ? withoutKnown(why) : withDetail(why.detail ?? (proc.lastError || dest.error))),
      };
      return { face, issue: asIssue("failed", face) };
    }
    case "stopped":
    default:
      if (dest.enabled && dest.transitioning) {
        return { face: { tone: "warn", label: "streamdeck.state.starting" } };
      }
      return { face: { tone: "idle", label: "streamdeck.state.off" } };
  }
}

function withDetail(detail: string | undefined): Pick<KeyFace, "detail"> {
  return detail ? { detail } : {};
}

function stormCount(dest: DestStatus): number {
  const n = dest.restartStorm;
  return typeof n === "number" && n > 0 ? n : 0;
}

/** Platform names as the platforms write them. Anything else keeps the server's word. */
const PLATFORM_NAME: Record<string, string> = { youtube: "YouTube", twitch: "Twitch", facebook: "Facebook", kick: "Kick" };

const SOURCE_ISSUE: Partial<Record<SourceKind, StreamDeckKey>> = {
  backup: "streamdeck.issue.sourceBackup",
  playlist: "streamdeck.issue.sourcePlaylist",
  slate: "streamdeck.issue.sourceSlate",
};

/**
 * The issues the server reports beside the process state. Each is the server's
 * own verdict, read off one field: the plugin judges no threshold here.
 */
function signalIssues(dest: DestStatus, source: SourceContext | undefined, stormOnFace: boolean | undefined): KeyIssue[] {
  const out: KeyIssue[] = [];
  const running = dest.process?.state === "running";

  // The account: before going live as much as during, because a key that is
  // off with an expired account will fail the moment it is pressed.
  if (dest.accountHealth === "expired" || dest.accountHealth === "refreshFailing") {
    out.push({ kind: "account", tone: "error", key: "streamdeck.issue.reconnectAccount" });
  } else if (dest.accountHealth === "expiring") {
    out.push({ kind: "accountExpiring", tone: "warn", key: "streamdeck.issue.accountExpiring" });
  }

  const storm = stormCount(dest);
  if (storm && !stormOnFace) {
    out.push({ kind: "restartStorm", tone: "warn", key: "streamdeck.issue.restartStorm", vars: { count: storm } });
  }

  // The programme's source persists across this destination's restarts, so it
  // stays in the list while the destination is between runs, not only while up.
  const onAir = running || dest.process?.state === "reconnecting";
  const src = onAir ? sourceIssue(dest, source) : undefined;

  // The rest are about a stream that is up right now: with nothing running there
  // is nothing to drop and nothing for the platform to judge.
  if (!running) {
    if (src) out.push(src);
    return out;
  }

  if (typeof dest.droppingFrames === "number" && dest.droppingFrames > 0) {
    out.push({ kind: "droppingFrames", tone: "warn", key: "streamdeck.issue.droppingFrames", note: String(dest.droppingFrames) });
  }

  const ph = dest.platformHealth;
  if (ph) {
    const name = PLATFORM_NAME[ph.platform] ?? ph.platform;
    out.push({
      kind: "platform",
      tone: "warn",
      key: "streamdeck.issue.platformPoor",
      vars: { platform: name },
      ...(ph.issues && ph.issues.length > 0 ? { note: ph.issues.join(", ") } : {}),
    });
  }

  if (src) out.push(src);
  return out;
}

/**
 * The programme's source, on every destination it feeds. Only when the payload's
 * failover is THIS destination's programme: an unscoped /status carries the
 * default programme's failover beside every programme's destinations (#543), and
 * a legacy row with no sourceId cannot be placed at all.
 */
function sourceIssue(dest: DestStatus, source: SourceContext | undefined): KeyIssue | undefined {
  const f = source?.failover;
  if (!f || f.error || dest.sourceId === undefined || source?.sourceId !== dest.sourceId) return undefined;
  const off = SOURCE_ISSUE[f.active];
  if (off) return { kind: "source", tone: "warn", key: off, ...withDetail(f.reason) };
  // #6: the primary is still on air, but its link is measured degraded. An
  // optional field, absent until that change ships, and absent while healthy.
  if (typeof f.degraded === "string" && f.degraded !== "") {
    return { kind: "source", tone: "warn", key: "streamdeck.issue.sourceDegraded", detail: f.degraded };
  }
  return undefined;
}

const REASON_LABEL: Record<FailureCause, StreamDeckKey> = {
  key_rejected: "streamdeck.reason.key_rejected",
  platform_not_ready: "streamdeck.reason.platform_not_ready",
  dns: "streamdeck.reason.dns",
  connection_refused: "streamdeck.reason.connection_refused",
  timeout: "streamdeck.reason.timeout",
  tls: "streamdeck.reason.tls",
  codec_rejected: "streamdeck.reason.codec_rejected",
  upload_too_slow: "streamdeck.reason.upload_too_slow",
  unknown: "streamdeck.reason.unknown",
};

interface Why {
  /** The cause is one this plugin has a label for. */
  known: boolean;
  reason?: StreamDeckKey;
  detail?: string;
}

/** The server's reason as a face fragment. A rotated key outranks the cause it
 *  explains, because it names the fix. "unknown" keeps the server's line. */
function reasonFor(dest: DestStatus): Why {
  const r = dest.reason;
  if (!r) return { known: false };
  const label = Object.hasOwn(REASON_LABEL, r.cause) ? REASON_LABEL[r.cause] : undefined;
  if (!label) return { known: false, detail: r.line };
  return {
    known: true,
    reason: r.keyChanged ? "streamdeck.reason.keyChanged" : label,
    detail: r.cause === "unknown" ? r.line : undefined,
  };
}

function withoutKnown(why: Why): Pick<KeyFace, "reason" | "detail"> {
  const out: Pick<KeyFace, "reason" | "detail"> = {};
  if (why.reason) out.reason = why.reason;
  if (why.detail) out.detail = why.detail;
  return out;
}

/** The one warning a destination key can carry, in priority order. */
function warningFor(dest: DestStatus): { label: StreamDeckKey; detail?: string } | null {
  if (dest.stopWarning) return { label: "streamdeck.warn.stopWarning", detail: dest.stopWarning };
  const backupDown = dest.backupProcess && dest.backupProcess.state !== "running";
  if (backupDown || dest.backupError) {
    return { label: "streamdeck.warn.backupDown", detail: dest.backupError || dest.backupProcess?.lastError };
  }
  return null;
}

function withWarning(face: KeyFace, warn: { label: StreamDeckKey; detail?: string } | null): KeyFace {
  if (!warn) return face;
  return { ...face, warning: warn.label, warningDetail: warn.detail };
}

const SOURCE_LABEL: Record<SourceKind, StreamDeckKey> = {
  "": "streamdeck.failover.unknown",
  primary: "streamdeck.failover.primary",
  backup: "streamdeck.failover.backup",
  playlist: "streamdeck.failover.playlist",
  slate: "streamdeck.failover.slate",
};

/** The failover key: what is on air right now, from the selector's own answer. */
export function failoverFace(f: FailoverStatus | null | undefined): KeyFace {
  if (!f) return { tone: "idle", label: "streamdeck.failover.unknown" };
  if (f.error) {
    return { tone: "error", label: SOURCE_LABEL[f.active] ?? SOURCE_LABEL[""], detail: f.error };
  }
  const label = SOURCE_LABEL[f.active] ?? SOURCE_LABEL[""];
  if (f.active === "primary") return { tone: "live", label };
  if (f.active === "") return { tone: "idle", label };
  return { tone: "warn", label, detail: f.reason };
}
