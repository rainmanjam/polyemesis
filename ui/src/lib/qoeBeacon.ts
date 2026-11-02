import type { BeaconError, BeaconKind, BeaconV1 } from "./types";

/** The public player's QoE reporting. The schema is BeaconV1 (types.ts), the
 *  server's half is internal/playout/qoe.go, and docs/API.md "QoE beacons"
 *  says what each field means.
 *
 *  What this file must never do is put anything identifying into a beacon: no
 *  URL, no error message, no user agent. Errors are reduced to a closed set of
 *  classes here, on the client, because an hls.js error carries the fragment
 *  URL and the fragment URL carries the playback token. */

/** The server's bound on startupMs (internal/playout/qoe.go
 *  MaxBeaconStartupMs). Past it the number is left off rather than sent: the
 *  server would only ignore it. */
export const MAX_STARTUP_MS = 10 * 60 * 1000;

/** How often an open page reports. */
export const HEARTBEAT_MS = 15_000;

/** A random per-page-load id: 16 bytes, hex. Never stored by the page. */
export function newSessionId(
  rand: (n: number) => Uint8Array = (n) => crypto.getRandomValues(new Uint8Array(n)),
): string {
  const bytes = rand(16);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

/** The fields of an hls.js error event this file reads. Typed locally so the
 *  mapping can be tested without constructing hls.js objects. */
export interface HlsErrorLike {
  type?: string;
  details?: string;
  fatal?: boolean;
}

/** Reduce an hls.js error to a beacon error class. Only the type and the
 *  details code are read -- never `url`, `response`, `reason` or `error`. */
export function classifyHlsError(e: HlsErrorLike): BeaconError {
  const details = e.details ?? "";
  if (/incompatiblecodecs|codec/i.test(details)) return "codec_unsupported";
  if (/^manifest|^level(Load|Empty|Parsing)/i.test(details)) return "manifest";
  if (/bufferAppend|fragParsing|fragDecrypt|bufferAddCodec/i.test(details)) return "decode";
  switch (e.type) {
    case "networkError":
      return "network";
    case "mediaError":
      return "media";
  }
  return "other";
}

/** A <video> element's MediaError code as a beacon class. */
export function classifyMediaError(code: number | undefined): BeaconError {
  switch (code) {
    case 2:
      return "network";
    case 3:
      return "decode";
    case 4:
      return "codec_unsupported";
  }
  return "media";
}

const MAX_ERRORS = 8;

/** QoETracker turns player events into beacons. It holds no timer of its own:
 *  the page calls `beacon()` on its heartbeat and on the way out, so a test
 *  drives it with a fake clock and nothing waits. */
export class QoETracker {
  private readonly sid: string;
  private readonly now: () => number;
  private readonly send: (b: BeaconV1) => void;
  private seq = 0;
  private createdAt: number;
  private startupMs?: number;
  private hasStarted = false;
  private stalledSince?: number;
  private seeking = false;
  private rebufferCount = 0;
  private rebufferMs = 0;
  private variant?: string;
  private latencyMs?: number;
  private pending: BeaconError[] = [];
  private ended = false;

  constructor(opts: { send: (b: BeaconV1) => void; now?: () => number; sid?: string }) {
    this.send = opts.send;
    this.now = opts.now ?? (() => performance.now());
    this.sid = opts.sid ?? newSessionId();
    this.createdAt = this.now();
  }

  /** Restart the startup clock: the player was (re)created. */
  reset(): void {
    if (!this.hasStarted) this.createdAt = this.now();
  }

  /** The video is rendering frames. The first call fixes the startup time; a
   *  later one closes a stall. */
  playing(): void {
    const t = this.now();
    if (!this.hasStarted) {
      this.hasStarted = true;
      const ms = Math.max(0, Math.round(t - this.createdAt));
      if (ms <= MAX_STARTUP_MS) this.startupMs = ms;
      return;
    }
    if (this.stalledSince !== undefined) {
      this.rebufferMs += Math.max(0, Math.round(t - this.stalledSince));
      this.stalledSince = undefined;
    }
  }

  /** The video stopped for want of data. Only a stall after startup and not
   *  caused by the viewer seeking counts as a rebuffer. */
  waiting(): void {
    if (!this.hasStarted || this.seeking || this.stalledSince !== undefined) return;
    this.stalledSince = this.now();
    this.rebufferCount++;
  }

  seekStarted(): void {
    this.seeking = true;
  }

  seekEnded(): void {
    this.seeking = false;
  }

  setVariant(name: string | undefined): void {
    this.variant = name || undefined;
  }

  setLatency(seconds: number | undefined): void {
    this.latencyMs =
      seconds === undefined || !Number.isFinite(seconds) || seconds < 0 ? undefined : Math.round(seconds * 1000);
  }

  error(code: BeaconError): void {
    if (this.pending.length < MAX_ERRORS) this.pending.push(code);
  }

  /** Whether a startup time is known; the page sends `start` once it is. */
  get started(): boolean {
    return this.hasStarted;
  }

  /** Build and send one beacon. After `end`, nothing more is sent. */
  beacon(kind: BeaconKind): BeaconV1 | undefined {
    if (this.ended) return undefined;
    if (kind === "end") this.ended = true;
    // A stall still open is reported up to now, so the last beacon of a page
    // closed mid-stall is not missing its worst moment.
    let rebufferMs = this.rebufferMs;
    if (this.stalledSince !== undefined) rebufferMs += Math.max(0, Math.round(this.now() - this.stalledSince));
    const b: BeaconV1 = {
      v: 1,
      sid: this.sid,
      seq: ++this.seq,
      kind,
      rebufferCount: this.rebufferCount,
      rebufferMs,
    };
    // On every beacon once known: the server counts it once per session, so
    // repeating it costs a few bytes and survives the first beacon being lost.
    if (this.startupMs !== undefined) b.startupMs = this.startupMs;
    if (this.variant) b.variant = this.variant;
    if (this.latencyMs !== undefined) b.latencyMs = this.latencyMs;
    if (this.pending.length > 0) {
      b.errors = this.pending;
      this.pending = [];
    }
    this.send(b);
    return b;
  }
}
