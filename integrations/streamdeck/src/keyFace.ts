import type { DestStatus, FailoverStatus, HoldStatus, SourceKind } from "./model.js";
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

  switch (proc.state) {
    case "running":
      if (dest.stalled) {
        return withWarning({ tone: "warn", label: "streamdeck.state.stalled" }, warn);
      }
      return withWarning({ tone: "live", label: "streamdeck.state.live" }, warn);
    case "starting":
      return withWarning({ tone: "warn", label: "streamdeck.state.starting" }, warn);
    case "reconnecting":
      return withWarning({ tone: "warn", label: "streamdeck.state.reconnecting", detail: proc.lastError }, warn);
    case "failed":
      // The reason is the process's own last error. It is shown verbatim
      // because the server has no structured "key rejected" code to map to.
      return withWarning(
        { tone: "error", label: "streamdeck.state.failed", detail: proc.lastError || dest.error },
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
