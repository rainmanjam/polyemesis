import { describe, expect, it } from "vitest";
import { hasDvrWindow, isBehindLive, levelsToRemove, qualityOptions } from "./playerControls";

const hi = { name: "hi", height: 1080, url: ["http://x/playout/hi/index.m3u8"] };
const lo = { name: "lo", height: 360, url: ["http://x/playout/lo/index.m3u8"] };
const hevc = { name: "4k", height: 2160, url: ["http://x/playout/4k/index.m3u8"] };

describe("levelsToRemove", () => {
  it("removes the undecodable rungs, highest index first", () => {
    expect(levelsToRemove([lo, hevc, hi, { ...hevc, name: "av1", url: ["http://x/playout/av1/index.m3u8"] }], new Set(["4k", "av1"]))).toEqual([3, 1]);
  });

  it("matches a level by its playlist URL when hls.js gave it no name", () => {
    expect(levelsToRemove([lo, { height: 2160, url: ["http://x/playout/4k/index.m3u8"] }], new Set(["4k"]))).toEqual([1]);
  });

  it("never empties the ladder", () => {
    expect(levelsToRemove([hevc], new Set(["4k"]))).toEqual([]);
    expect(levelsToRemove([hevc, { ...hevc, name: "b", url: ["/playout/b/index.m3u8"] }], new Set(["4k", "b"]))).toEqual([1]);
  });

  it("removes nothing when nothing was dropped", () => {
    expect(levelsToRemove([lo, hi], new Set())).toEqual([]);
  });
});

describe("qualityOptions", () => {
  it("labels by height, and by name where heights collide or are missing", () => {
    expect(qualityOptions([lo, hi, { name: "hi2", height: 1080 }, { name: "src" }, {}])).toEqual([
      { index: 0, label: "360p" },
      { index: 1, label: "hi" },
      { index: 2, label: "hi2" },
      { index: 3, label: "src" },
      { index: 4, label: "5" },
    ]);
  });
});

describe("live edge and DVR window", () => {
  it("offers Go live only when more than a few seconds behind", () => {
    expect(isBehindLive(null)).toBe(false);
    expect(isBehindLive({ start: 0, end: 100, at: 95, edge: 96 })).toBe(false);
    expect(isBehindLive({ start: 0, end: 100, at: 87, edge: 96 })).toBe(true);
  });

  it("draws a seek bar only over a real replay window", () => {
    expect(hasDvrWindow(null)).toBe(false);
    expect(hasDvrWindow({ start: 80, end: 100, at: 99, edge: 99 })).toBe(false);
    expect(hasDvrWindow({ start: 40, end: 100, at: 99, edge: 99 })).toBe(true);
  });
});
