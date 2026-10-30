import type { LevelsFrame, Feed } from "./model.js";
import type { StreamDeckKey } from "./keys.js";

// The dial face. Freshness is the SERVER's verdict, read from GET /levels as the
// `feed` field, so the window rule lives in exactly one place
// (internal/api/levels_feed.go). This module only draws what the server said:
// a dial with no live reading shows NO FEED instead of a frozen bar.

/** Dial readings at or below this are digital silence, the server's floor. */
export const SILENCE_DB = -100;
/** Full-scale meter range. A reading at -60 dBFS draws an empty bar. */
const FLOOR_DB = -60;

export interface MeterFace {
  tone: "live" | "quiet" | "nofeed";
  /** 0..1 bar height. Zero for quiet and no-feed. */
  value: number;
  /** The loudest channel in dBFS, when there is a reading. */
  peakDb?: number;
  label?: StreamDeckKey;
}

/** The face of a dial for the server's feed state and the reading it sent with it. */
export function meterFace(feed: Feed, frame: LevelsFrame | null | undefined): MeterFace {
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
