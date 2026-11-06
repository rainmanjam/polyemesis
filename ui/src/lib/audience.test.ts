import { describe, expect, it } from "vitest";

import { formatAverage, orderReadings, readingCount, sparkPath, sumReported } from "@/lib/audience";

describe("audience helpers", () => {
  it("never turns a missing count into a zero", () => {
    expect(readingCount({ platform: "kick", live: true, reason: "hidden" })).toBeNull();
    expect(readingCount({ platform: "kick", live: true, viewers: 0 })).toBe(0);
    expect(sumReported([{ platform: "kick", live: true, reason: "hidden" }])).toBeNull();
    expect(
      sumReported([
        { platform: "kick", live: true, reason: "hidden" },
        { platform: "twitch", live: true, viewers: 4 },
        { platform: "player", live: true, viewers: 0 },
      ]),
    ).toBe(4);
  });

  it("puts the player last", () => {
    const got = orderReadings([
      { platform: "player", live: true, viewers: 1 },
      { platform: "youtube", live: true, name: "b" },
      { platform: "twitch", live: true, name: "a" },
    ]).map((r) => r.platform);
    expect(got).toEqual(["twitch", "youtube", "player"]);
  });

  it("formats averages", () => {
    expect(formatAverage(20)).toBe("20");
    expect(formatAverage(38.66)).toBe("38.7");
  });

  it("draws a gap where nothing reported, not a dip to zero", () => {
    const d = sparkPath(
      [
        { at: "a", total: 10 },
        { at: "b", total: 5 },
        { at: "c" },
        { at: "d", total: 10 },
        { at: "e", total: 0 },
      ],
      40,
      10,
    );
    // Two runs: M..L then a gap, then M..L again. No point is drawn for "c".
    expect(d.match(/M/g)?.length).toBe(2);
    expect(d).toBe("M0.0 0.0 L10.0 5.0 M30.0 0.0 L40.0 10.0");
    expect(sparkPath([{ at: "a" }], 10, 10)).toBe("");
    expect(sparkPath([], 10, 10)).toBe("");
    // A lone reported point between gaps is still visible.
    expect(sparkPath([{ at: "a" }, { at: "b", total: 3 }, { at: "c" }], 20, 10)).toBe("M10.0 0.0 h0.1");
  });
});
