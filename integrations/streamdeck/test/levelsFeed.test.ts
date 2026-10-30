import { describe, expect, it } from "vitest";
import { meterFace } from "../src/levelsFeed.js";
import type { LevelsFrame } from "../src/model.js";

const frame = (peak: number[][]): LevelsFrame => ({ peak, rms: peak });

describe("meterFace", () => {
  it("shows NO FEED when the server reports no feed, rather than a frozen bar", () => {
    expect(meterFace("none", null)).toEqual({ tone: "nofeed", value: 0, label: "streamdeck.meter.noFeed" });
  });

  it("shows NO FEED when the server reports the feed stale, even with a reading to hand", () => {
    expect(meterFace("stale", frame([[-10]])).tone).toBe("nofeed");
  });

  it("shows NO FEED for a live feed that carries no reading", () => {
    expect(meterFace("live", null).tone).toBe("nofeed");
  });

  it("shows QUIET at the server's silence floor, not an empty live bar", () => {
    expect(meterFace("live", frame([[-100, -100]]))).toMatchObject({
      tone: "quiet",
      value: 0,
      label: "streamdeck.meter.quiet",
    });
  });

  it("scales the loudest channel from -60 dBFS (empty) to 0 dBFS (full)", () => {
    expect(meterFace("live", frame([[-60]])).value).toBe(0);
    expect(meterFace("live", frame([[0]])).value).toBe(1);
    expect(meterFace("live", frame([[-30]])).value).toBe(0.5);
  });

  it("uses the loudest channel across all tracks", () => {
    expect(meterFace("live", frame([[-50, -40], [-12]])).peakDb).toBe(-12);
  });

  it("clamps a reading above full scale", () => {
    expect(meterFace("live", frame([[6]])).value).toBe(1);
  });

  it("treats a frame with no numeric channels as no feed", () => {
    expect(meterFace("live", frame([[]])).tone).toBe("nofeed");
  });
});
