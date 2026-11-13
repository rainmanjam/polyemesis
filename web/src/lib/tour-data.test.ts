import { existsSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { TOUR, clock } from "../data/tour.mjs";

const pub = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "public");

describe("the video tour data", () => {
  it("has three chapters", () => expect(TOUR.chapters).toHaveLength(3));

  it("starts at zero, with strictly increasing starts, all inside the video", () => {
    const starts = TOUR.chapters.map((c) => c.start);
    expect(starts[0]).toBe(0);
    for (let i = 1; i < starts.length; i++) expect(starts[i]).toBeGreaterThan(starts[i - 1]!);
    for (const s of starts) expect(s).toBeLessThan(TOUR.duration);
  });

  it("names the chapters in sentence case", () => {
    expect(TOUR.chapters.map((c) => c.title)).toEqual(["One stream in", "Splitting the audio", "Going live everywhere"]);
  });

  it("points at files under /media/ that exist", () => {
    expect(TOUR.src).toBe("/media/tour.mp4");
    expect(TOUR.poster).toBe("/media/tour-poster.jpg");
    expect(existsSync(join(pub, TOUR.src))).toBe(true);
    expect(existsSync(join(pub, TOUR.poster))).toBe(true);
  });

  it("keeps the video within 8,000,000 bytes", () => {
    expect(statSync(join(pub, TOUR.src)).size).toBeLessThanOrEqual(8_000_000);
  });
});

describe("clock", () => {
  it("writes seconds as m:ss, rounding down", () => {
    expect(clock(0)).toBe("0:00");
    expect(clock(22.2)).toBe("0:22");
    expect(clock(58.6)).toBe("0:58");
    expect(clock(60)).toBe("1:00");
    expect(clock(99.3)).toBe("1:39");
  });
});
