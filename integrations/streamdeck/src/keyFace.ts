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
  /** The server's classified cause, as a catalogue key. A translation of the
   *  server's code, not an inference made here. */
  reason?: StreamDeckKey;
  /** Server words, passed through unchanged. Never invented here. */
  detail?: string;
  /** A second, independent problem worth a badge, e.g. a dead backup. */
  warning?: StreamDeckKey;
  /** Server words for the warning, when the warning is the server's own text. */
  warningDetail?: string;
}

/** The face of a destination key bound to one destination and, optionally, one programme. */
export function destinationFace(
  dest: DestStatus | undefined,
  boundSourceId: number | undefined,
  hold: HoldStatus | null | undefined,
): KeyFace {
  if (!dest) {
    return { tone: "unbound", label: "streamdeck.state.missing" };
  }
  // A key bound to programme A must never show the state of a destination on
  // programme B. Showing it would look like a fact about A.
  if (boundSourceId !== undefined && dest.sourceId !== boundSourceId) {
    return { tone: "unbound", label: "streamdeck.state.otherProgramme" };
  }

  const warn = warningFor(dest);
  const proc = dest.process ?? null;

  if (!proc) {
    if (!dest.enabled) return withWarning({ tone: "idle", label: "streamdeck.state.off" }, warn);
    if (hold) {
      return withWarning({ tone: "held", label: "streamdeck.state.held", detail: hold.reason }, warn);
    }
    // Enabled with no process: the server is between states, not off.
    return withWarning({ tone: "warn", label: "streamdeck.state.starting" }, warn);
  }

  // The server's classified cause (#20). A key known to this plugin replaces
  // the raw FFmpeg tail, which on 72 px is a clipped exit status; an unknown
  // cause, or a code newer than this plugin, keeps the server's own words.
  const why = reasonFor(dest);

  switch (proc.state) {
    case "running":
      if (dest.stalled) {
        return withWarning({ tone: "warn", label: "streamdeck.state.stalled" }, warn);
      }
      // A slow upload does not exit, so it is the one cause shown on a running
      // destination. Live, in the warn colour: it IS on air, badly.
      if (why.reason === "streamdeck.reason.upload_too_slow") {
        return withWarning({ tone: "warn", label: "streamdeck.state.live", reason: why.reason }, warn);
      }
      return withWarning({ tone: "live", label: "streamdeck.state.live" }, warn);
    case "starting":
      return withWarning({ tone: "warn", label: "streamdeck.state.starting" }, warn);
    case "reconnecting":
      return withWarning(
        { tone: "warn", label: "streamdeck.state.reconnecting", ...(why.known ? why : { detail: why.detail ?? proc.lastError }) },
        warn,
      );
    case "failed":
      // Held is its own tile: the server has stopped retrying, and only a
      // Retry from the console will change what this key shows.
      if (dest.reason?.held) {
        return withWarning({ tone: "held", label: "streamdeck.state.held", ...withoutKnown(why) }, warn);
      }
      return withWarning(
        {
          tone: "error",
          label: "streamdeck.state.failed",
          ...(why.known ? withoutKnown(why) : { detail: why.detail ?? (proc.lastError || dest.error) }),
        },
        warn,
      );
    case "stopped":
    default:
      if (dest.enabled && dest.transitioning) {
        return withWarning({ tone: "warn", label: "streamdeck.state.starting" }, warn);
      }
      return withWarning({ tone: "idle", label: "streamdeck.state.off" }, warn);
  }
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
  // A degraded primary that had nowhere to go is still on air, and the key
  // must not read as a healthy "live": the link is breaking up.
  if (f.active === "primary" && f.degraded) {
    return { tone: "warn", label: "streamdeck.failover.degraded", detail: f.degraded };
  }
  if (f.active === "primary") return { tone: "live", label };
  if (f.active === "") return { tone: "idle", label };
  return { tone: "warn", label, detail: f.reason };
}
