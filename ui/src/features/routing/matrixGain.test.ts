import { describe, expect, it } from "vitest";
import { gainDbLabel, gainState } from "./matrixGain";

/* A15: a matrix cell is a linear gain, but an audio operator reads dB. The
 * label must say what the number means at a glance: 0 is a removed connection,
 * 1 is unity (0 dB), 0.707 is the -3 dB every mixer knows. */
describe("gainDbLabel", () => {
  it("reads zero as off rather than minus infinity", () => {
    expect(gainDbLabel(0)).toBe("off");
  });

  it("reads unity as 0.0 dB", () => {
    expect(gainDbLabel(1)).toBe("0.0 dB");
  });

  it("reads 0.707 as -3.0 dB", () => {
    expect(gainDbLabel(0.707)).toBe("-3.0 dB");
  });

  it("reads gain 2 as +6.0 dB", () => {
    expect(gainDbLabel(2)).toBe("+6.0 dB");
  });
});

describe("gainState", () => {
  it("classifies zero as off", () => {
    expect(gainState(0)).toBe("off");
  });

  it("classifies unity as unity, tolerant of float noise", () => {
    expect(gainState(1)).toBe("unity");
    expect(gainState(0.9999)).toBe("unity");
  });

  it("classifies anything else as custom", () => {
    expect(gainState(0.707)).toBe("custom");
    expect(gainState(1.5)).toBe("custom");
  });
});
