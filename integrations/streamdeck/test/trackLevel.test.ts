import { describe, expect, it } from "vitest";
import { barFor, DB_MAX, DB_MIN, DB_STEP, formatDb, gainForDb, gainToDb, MAX_GAIN, stepDb } from "../src/trackLevel.js";

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
    expect(stepDb(-60, 1)).toBe(DB_MIN);
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

  it("draws the bar across the dial's range", () => {
    expect(barFor(DB_MIN)).toBe(0);
    expect(barFor(DB_MAX)).toBe(100);
    expect(barFor(-100)).toBe(0);
    expect(barFor(0)).toBe(87);
  });
});
