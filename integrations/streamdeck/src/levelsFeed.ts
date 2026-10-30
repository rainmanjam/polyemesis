import type { LevelsFrame } from "./model.js";
import type { StreamDeckKey } from "./keys.js";

// The dial meter. The server pushes levels over the WebSocket at the meter
// interval the install is set to. A dial that has not heard from the server
// recently must say NO FEED rather than hold a frozen bar, so this module owns
// the freshness judgement.
//
// The window matches internal/api/levels_feed.go exactly: the larger of three
// meter intervals and one second, and a reading exactly at the window is still
// live. The WebSocket frame carries no timestamp, so arrival time on this side
// is the clock.

/** Dial readings at or below this are digital silence, the server's floor. */
export const SILENCE_DB = -100;
/** Full-scale meter range. A reading at -60 dBFS draws an empty bar. */
const FLOOR_DB = -60;

export type Feed = "none" | "live" | "stale";

export function feedWindowMs(intervalMs: number): number {
  return Math.max(3 * intervalMs, 1000);
}

export class LevelsFeed {
  private arrivedAt: number | undefined;
  private frame: LevelsFrame | undefined;

  constructor(
    private intervalMs: number,
    private readonly now: () => number = Date.now,
  ) {}

  setInterval(intervalMs: number): void {
    this.intervalMs = intervalMs;
  }

  onFrame(frame: LevelsFrame): void {
    this.frame = frame;
    this.arrivedAt = this.now();
  }

  state(): Feed {
    if (this.arrivedAt === undefined) return "none";
    return this.now() - this.arrivedAt <= feedWindowMs(this.intervalMs) ? "live" : "stale";
  }

  /** The latest frame, but only while the feed is live. A stale frame is not a reading. */
  current(): LevelsFrame | undefined {
    return this.state() === "live" ? this.frame : undefined;
  }
}

export interface MeterFace {
  tone: "live" | "quiet" | "nofeed";
  /** 0..1 bar height. Zero for quiet and no-feed. */
  value: number;
  /** The loudest channel in dBFS, when there is a reading. */
  peakDb?: number;
  label?: StreamDeckKey;
}

/** The face of a dial for one feed state and one frame. */
export function meterFace(feed: Feed, frame: LevelsFrame | undefined): MeterFace {
  const peaks = (frame?.peak ?? []).flat().filter((v) => Number.isFinite(v));
  if (feed !== "live" || peaks.length === 0) {
    return { tone: "nofeed", value: 0, label: "streamdeck.meter.noFeed" };
  }
  const peakDb = Math.max(...peaks);
  if (peakDb <= SILENCE_DB) {
    return { tone: "quiet", value: 0, peakDb, label: "streamdeck.meter.quiet" };
  }
  const value = Math.min(1, Math.max(0, (peakDb - FLOOR_DB) / -FLOOR_DB));
  return { tone: "live", value, peakDb };
}

/**
 * Reads one WebSocket message and returns the levels frame it carries, or
 * undefined for any other event. Anything malformed is ignored rather than
 * guessed at: a frame the plugin cannot read must not refresh the feed.
 */
export function levelsFromMessage(raw: string): LevelsFrame | undefined {
  let msg: unknown;
  try {
    msg = JSON.parse(raw);
  } catch {
    return undefined;
  }
  if (typeof msg !== "object" || msg === null) return undefined;
  const { type, data } = msg as { type?: unknown; data?: unknown };
  if (type !== "levels" || typeof data !== "object" || data === null) return undefined;
  const { peak, rms } = data as { peak?: unknown; rms?: unknown };
  if (!isGrid(peak) || !isGrid(rms)) return undefined;
  return { peak, rms };
}

function isGrid(v: unknown): v is number[][] {
  return Array.isArray(v) && v.every((row) => Array.isArray(row) && row.every((n) => typeof n === "number"));
}
