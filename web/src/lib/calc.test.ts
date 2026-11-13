import { describe, expect, it } from "vitest";

import { HEADROOM_PCT, OVERHEAD_PCT, clampInt, egressKbps, fmtData, fmtRate, gbFor, ingestKbps, perMonth, withMargins } from "./calc";

describe("calc formulas", () => {
  it("ingest is the video plus one audio rate per track", () => {
    expect(ingestKbps({ video: 6000, audio: 160, tracks: 6 })).toBe(6960);
    expect(ingestKbps({ video: 6000, audio: 160, tracks: 1 })).toBe(6160);
  });

  it("egress sends the copied video and one audio pair to every destination", () => {
    expect(egressKbps({ video: 6000, audio: 160, dests: 5 })).toBe(30800);
    expect(egressKbps({ video: 6000, audio: 160, dests: 0 })).toBe(0);
  });

  it("margins multiply overhead by headroom", () => {
    expect(withMargins(1000, 5, 20)).toBeCloseTo(1260, 9);
    expect(withMargins(1000, 0, 0)).toBe(1000);
  });

  it("data uses overhead but not headroom, at 450 MB per Mbps-hour", () => {
    expect(gbFor(1000, 1, 0)).toBeCloseTo(0.45, 9);
    expect(gbFor(1000, 2, 0)).toBeCloseTo(0.9, 9);
    expect(gbFor(1000, 1, 10)).toBeCloseTo(0.495, 9);
  });

  it("a month is 4.35 weeks of streams", () => {
    expect(perMonth(10, 3)).toBeCloseTo(130.5, 9);
  });
});

describe("calc formatting", () => {
  it("prints Mbps with one decimal", () => {
    expect(fmtRate(7756.8)).toBe("7.8 Mbps");
  });
  it("switches to TB at a thousand GB", () => {
    expect(fmtData(23.26)).toBe("23.3 GB");
    expect(fmtData(1500)).toBe("1.50 TB");
  });
});

describe("clampInt", () => {
  it("passes an integer inside the range", () => {
    expect(clampInt("3", 1, 6, 1)).toBe(3);
    expect(clampInt(3, 1, 6, 1)).toBe(3);
  });
  it("holds at the minimum and the maximum", () => {
    expect(clampInt("1", 1, 6, 2)).toBe(1);
    expect(clampInt("6", 1, 6, 2)).toBe(6);
  });
  it("clamps below and above", () => {
    expect(clampInt("0", 1, 6, 2)).toBe(1);
    expect(clampInt("-9", 1, 6, 2)).toBe(1);
    expect(clampInt("7", 1, 6, 2)).toBe(6);
    expect(clampInt("1e9", 1, 6, 2)).toBe(6);
  });
  it("rounds a decimal to an integer", () => {
    expect(clampInt("2.5", 1, 6, 1)).toBe(3);
    expect(clampInt("2.4", 1, 6, 1)).toBe(2);
    expect(Number.isInteger(clampInt("5.9", 1, 6, 1))).toBe(true);
  });
  it("gives the fallback for empty or non-numeric text", () => {
    expect(clampInt("", 1, 6, 2)).toBe(2);
    expect(clampInt("  ", 1, 6, 2)).toBe(2);
    expect(clampInt("abc", 1, 6, 2)).toBe(2);
    expect(clampInt("Infinity", 1, 6, 2)).toBe(2);
  });
});

describe("shared margin defaults", () => {
  it("are what /calculator opens with", () => {
    expect([OVERHEAD_PCT, HEADROOM_PCT]).toEqual([5, 20]);
  });
});
