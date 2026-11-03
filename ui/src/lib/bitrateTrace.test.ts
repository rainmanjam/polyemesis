import { describe, expect, it } from "vitest";
import { BITRATE_GAP_MS, bitrateTrace } from "./bitrateTrace";

/* The Monitoring bitrate chart is a linear trace that BREAKS where samples are
 * missing. A line drawn straight across a hole invents a reading for a period
 * nobody measured, which on an ingest chart is exactly the period an operator
 * is trying to see (the encoder dropped, the server restarted). */

const at = (sec: number) => new Date(Date.UTC(2026, 10, 2, 12, 0, sec)).toISOString();
const ms = (sec: number) => Date.UTC(2026, 10, 2, 12, 0, sec);

describe("bitrateTrace", () => {
  it("passes a continuous series through, as numbers", () => {
    expect(
      bitrateTrace([
        { t: at(0), kbps: 3000.4 },
        { t: at(1), kbps: 3100.6 },
      ]),
    ).toEqual([
      { t: ms(0), kbps: 3000 },
      { t: ms(1), kbps: 3101 },
    ]);
  });

  it("inserts a null point inside a hole so the line breaks there", () => {
    const out = bitrateTrace([
      { t: at(0), kbps: 3000 },
      { t: at(1), kbps: 3000 },
      { t: at(30), kbps: 2900 },
    ]);
    expect(out).toHaveLength(4);
    expect(out[2].kbps).toBeNull();
    // Inside the hole, so neither neighbour is moved.
    expect(out[2].t).toBeGreaterThan(ms(1));
    expect(out[2].t).toBeLessThan(ms(30));
    expect(out[3]).toEqual({ t: ms(30), kbps: 2900 });
  });

  it("does not break the line for normal jitter in a one-second cadence", () => {
    const out = bitrateTrace([
      { t: at(0), kbps: 1 },
      { t: new Date(ms(0) + BITRATE_GAP_MS).toISOString(), kbps: 1 },
    ]);
    expect(out.every((p) => p.kbps !== null)).toBe(true);
  });

  it("breaks once the gap is longer than the threshold", () => {
    const out = bitrateTrace([
      { t: at(0), kbps: 1 },
      { t: new Date(ms(0) + BITRATE_GAP_MS + 1).toISOString(), kbps: 1 },
    ]);
    expect(out.some((p) => p.kbps === null)).toBe(true);
  });

  it("returns nothing for nothing", () => {
    expect(bitrateTrace([])).toEqual([]);
  });
});
