import { describe, expect, it } from "vitest";
import { LevelsFeed, feedWindowMs, levelsFromMessage, meterFace } from "../src/levelsFeed.js";
import type { LevelsFrame } from "../src/model.js";

function clock(start = 1_000_000) {
  let t = start;
  return { now: () => t, advance: (ms: number) => (t += ms) };
}

const frame = (peak: number[][]): LevelsFrame => ({ peak, rms: peak });

describe("feedWindowMs", () => {
  it("is three meter intervals, with a one-second floor", () => {
    expect(feedWindowMs(100)).toBe(1000);
    expect(feedWindowMs(500)).toBe(1500);
    expect(feedWindowMs(2000)).toBe(6000);
  });
});

describe("LevelsFeed freshness", () => {
  it("is none before any frame has arrived", () => {
    const c = clock();
    expect(new LevelsFeed(100, c.now).state()).toBe("none");
  });

  it("is live for a frame inside the window", () => {
    const c = clock();
    const feed = new LevelsFeed(100, c.now);
    feed.onFrame(frame([[-20]]));
    c.advance(999);
    expect(feed.state()).toBe("live");
  });

  it("is live exactly at the window, matching the server's <= boundary", () => {
    const c = clock();
    const feed = new LevelsFeed(100, c.now);
    feed.onFrame(frame([[-20]]));
    c.advance(1000);
    expect(feed.state()).toBe("live");
  });

  it("is stale one millisecond past the window, and then gives no frame", () => {
    const c = clock();
    const feed = new LevelsFeed(100, c.now);
    feed.onFrame(frame([[-20]]));
    c.advance(1001);
    expect(feed.state()).toBe("stale");
    expect(feed.current()).toBeUndefined();
  });

  it("widens the window when the install's meter interval is longer", () => {
    const c = clock();
    const feed = new LevelsFeed(500, c.now);
    feed.onFrame(frame([[-20]]));
    c.advance(1400);
    expect(feed.state()).toBe("live");
    feed.setInterval(100);
    expect(feed.state()).toBe("stale");
  });

  it("recovers to live on the next frame", () => {
    const c = clock();
    const feed = new LevelsFeed(100, c.now);
    feed.onFrame(frame([[-20]]));
    c.advance(5000);
    feed.onFrame(frame([[-10]]));
    expect(feed.state()).toBe("live");
  });
});

describe("meterFace", () => {
  it("shows NO FEED with no reading, rather than a frozen bar", () => {
    expect(meterFace("none", undefined)).toEqual({ tone: "nofeed", value: 0, label: "streamdeck.meter.noFeed" });
  });

  it("shows NO FEED when the feed is stale, even with an old frame to hand", () => {
    expect(meterFace("stale", frame([[-10]])).tone).toBe("nofeed");
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

describe("levelsFromMessage", () => {
  it("reads a levels event off the WebSocket envelope", () => {
    const raw = JSON.stringify({ type: "levels", time: "2026-10-29T12:00:00Z", data: { peak: [[-12]], rms: [[-20]] } });
    expect(levelsFromMessage(raw)).toEqual({ peak: [[-12]], rms: [[-20]] });
  });

  it("ignores every other event type", () => {
    expect(levelsFromMessage(JSON.stringify({ type: "status", data: {} }))).toBeUndefined();
  });

  it("ignores a frame that is not JSON or whose grids are malformed", () => {
    expect(levelsFromMessage("not json")).toBeUndefined();
    expect(levelsFromMessage(JSON.stringify({ type: "levels", data: { peak: "loud", rms: [] } }))).toBeUndefined();
    expect(levelsFromMessage(JSON.stringify({ type: "levels", data: { peak: [["x"]], rms: [] } }))).toBeUndefined();
  });
});
