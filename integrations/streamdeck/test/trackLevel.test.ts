import { describe, expect, it } from "vitest";
import { barFor, DB_MAX, DB_MIN, DB_STEP, dbToGain, formatDb, gainForDb, gainToDb, MAX_GAIN, stepDb } from "../src/trackLevel.js";

describe("the dial's step", () => {
  it("moves 0.5 dB a tick, both ways, from unity", () => {
    expect(DB_STEP).toBe(0.5);
    expect(stepDb(0, 1)).toBe(0.5);
    expect(stepDb(0, -3)).toBe(-1.5);
  });

  it("lands an off-grid level on the next grid point in the direction of the turn", () => {
    // 0.8 linear is -1.94 dB, as the UI's percentage leaves it.
    const from = gainToDb(0.8);
    expect(stepDb(from, 1)).toBe(-1.5);
    expect(stepDb(from, -1)).toBe(-2);
  });

  it("treats a stored float a hair off a grid point as on it", () => {
    expect(stepDb(gainToDb(gainForDb(-3)), 1)).toBe(-2.5);
  });

  it("clamps to the dial's range, so a fast spin stops at the ends", () => {
    expect(stepDb(0, 1000)).toBe(DB_MAX);
    expect(stepDb(0, -1000)).toBe(DB_MIN);
  });

  it("never moves a level already past an end backwards", () => {
    // The server allows up to +6.02 dB (gain 2.0), above the dial's top step.
    const top = gainToDb(MAX_GAIN);
    expect(stepDb(top, 1)).toBe(top);
    expect(stepDb(top, -1)).toBe(DB_MAX);
    expect(stepDb(-60, -1)).toBe(-60);
  });

  // MUTATION: clamp the step to DB_MIN regardless of the level (the old clampDb). Observed: FAIL.
  it("steps a level below the dial's floor from where it is, not up to the floor", () => {
    // gain 0.001 is -60 dB: one tick up is -59.5, never a 20 dB jump to -40.
    expect(stepDb(gainToDb(0.001), 1)).toBeCloseTo(-59.5, 9);
    expect(stepDb(-60, 1)).toBe(-59.5);
    expect(stepDb(-60.2, 1)).toBe(-60);
    expect(stepDb(-60, 3)).toBe(-58.5);
    // A turn down from below the floor still goes nowhere.
    expect(stepDb(-60, -1)).toBe(-60);
  });

  it("does nothing for no ticks", () => {
    expect(stepDb(-7.3, 0)).toBe(-7.3);
  });
});

describe("the gain sent", () => {
  it("is unity at 0 dB and inside the server's (0, MaxGain] at every step of the dial", () => {
    expect(gainForDb(0)).toBe(1);
    for (let db = DB_MIN; db <= DB_MAX; db += DB_STEP) {
      const g = gainForDb(db);
      expect(g).toBeGreaterThan(0);
      expect(g).toBeLessThanOrEqual(MAX_GAIN);
    }
  });

  // MUTATION: raise the level to DB_MIN before converting. Observed: FAIL.
  it("sends a level below the dial's floor as that level", () => {
    expect(gainForDb(-59.5)).toBeCloseTo(dbToGain(-59.5), 6);
    expect(gainForDb(-59.5)).toBeLessThan(dbToGain(DB_MIN));
  });

  it("is clamped even when asked for more than the dial allows", () => {
    expect(gainForDb(40)).toBeLessThanOrEqual(MAX_GAIN);
    expect(gainForDb(-400)).toBeGreaterThan(0);
  });
});

describe("the dial's display", () => {
  it("prints the level with a sign and one decimal", () => {
    expect(formatDb(0)).toBe("0.0 dB");
    expect(formatDb(-0.04)).toBe("0.0 dB");
    expect(formatDb(1.5)).toBe("+1.5 dB");
    expect(formatDb(-12)).toBe("-12.0 dB");
  });

  it("prints a stored gain of 0 as minus infinity, with an empty bar, and turns it up to the dial's floor", () => {
    expect(formatDb(gainToDb(0))).toBe("-∞ dB");
    expect(barFor(gainToDb(0))).toBe(0);
    expect(stepDb(gainToDb(0), 1)).toBe(DB_MIN);
    expect(stepDb(gainToDb(0), -1)).toBe(Number.NEGATIVE_INFINITY);
  });

  it("draws the bar across the dial's range", () => {
    expect(barFor(DB_MIN)).toBe(0);
    expect(barFor(DB_MAX)).toBe(100);
    expect(barFor(-100)).toBe(0);
    expect(barFor(0)).toBe(87);
  });
});
