import { describe, expect, it } from "vitest";
import { autoZoomRange, dbFraction, formatPeakDb, FIXED_RANGE } from "./meterScale";

/* A16: the meter scale is fixed at -60..0, so a quiet source sits low and is hard
 * to judge. Auto zoom narrows the floor to what is being measured, but it must
 * always keep the -14 streaming reference and the -18 break on the scale. */
describe("autoZoomRange", () => {
  it("keeps the -14 and -18 references on the scale for any level", () => {
    for (const peak of [-60, -48, -30, -14, -6, -0.2, 0, 3]) {
      const { minDb } = autoZoomRange(peak);
      expect(minDb).toBeLessThanOrEqual(-18);
    }
  });

  it("ends at 0 dBFS and never below the fixed floor", () => {
    expect(autoZoomRange(-40).maxDb).toBe(0);
    expect(autoZoomRange(-200).minDb).toBe(FIXED_RANGE.minDb);
  });

  it("zooms in on a quiet source instead of keeping the full -60 floor", () => {
    expect(autoZoomRange(-40).minDb).toBeGreaterThan(FIXED_RANGE.minDb);
  });

  it("steps the floor in 6 dB increments so the scale does not jitter", () => {
    for (const peak of [-44, -37.5, -29.1, -12.7]) {
      expect(Number.isInteger(autoZoomRange(peak).minDb / 6)).toBe(true);
    }
  });

  it("treats no measurement (silence sentinel) as the full fixed range", () => {
    expect(autoZoomRange(-100)).toEqual(FIXED_RANGE);
  });
});

describe("formatPeakDb", () => {
  it("prints the hold to one decimal with its unit", () => {
    expect(formatPeakDb(-6.34)).toBe("-6.3 dB");
  });

  it("shows a dash rather than a number at the floor, where nothing was measured", () => {
    expect(formatPeakDb(-60)).toBe("—");
  });

  it("keeps the clip threshold readable at -0.2", () => {
    expect(formatPeakDb(-0.2)).toBe("-0.2 dB");
  });
});

describe("dbFraction", () => {
  it("places a level by the range it is drawn in", () => {
    expect(dbFraction(-14, { minDb: -24, maxDb: 0 })).toBeCloseTo(10 / 24);
    expect(dbFraction(-14, FIXED_RANGE)).toBeCloseTo(46 / 60);
  });

  it("clamps outside the range", () => {
    expect(dbFraction(-40, { minDb: -24, maxDb: 0 })).toBe(0);
    expect(dbFraction(5, { minDb: -24, maxDb: 0 })).toBe(1);
  });
});
