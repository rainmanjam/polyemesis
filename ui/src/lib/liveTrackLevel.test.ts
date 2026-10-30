import { describe, expect, it } from "vitest";
import {
  carryMutes,
  destinationLiveBlock,
  levelOutcome,
  trackLiveBlock,
  withLevel,
} from "./liveTrackLevel";
import type { Destination, RoutingProfile } from "./types";

const profile = (over: Partial<RoutingProfile> = {}): RoutingProfile =>
  ({
    mode: "simple",
    tracks: [
      { track: 0, enabled: true, gain: 1 },
      { track: 1, enabled: true, gain: 0.8, muted: true },
    ],
    matrix: null,
    normalize: "auto",
    ...over,
  }) as RoutingProfile;

const dest = (over: Partial<Destination> = {}) =>
  ({ kind: "rtmp", audio: {}, profile: profile(), ...over }) as Destination;

describe("live level availability", () => {
  it("a stored simple-mode track can take a live level", () => {
    expect(trackLiveBlock(dest(), 0)).toBeNull();
    expect(destinationLiveBlock(dest())).toBeNull();
  });

  it("an audio-copy destination cannot, as the route's 409 says", () => {
    const d = dest({ kind: "srt", audio: { copy: true } });
    expect(destinationLiveBlock(d)).toBe("route.live.copyBlocked");
    expect(trackLiveBlock(d, 0)).toBe("route.live.copyBlocked");
  });

  it("an Icecast row encodes, so copy does not block it", () => {
    const d = dest({ kind: "audio" as Destination["kind"], audio: { copy: true } });
    expect(destinationLiveBlock(d)).toBeNull();
  });

  it("a track absent from the STORED selection must be saved first (the route's 404)", () => {
    expect(trackLiveBlock(dest(), 4)).toBe("route.live.saveFirst");
  });

  it("a stored matrix mix must be saved as simple first", () => {
    expect(trackLiveBlock(dest({ profile: profile({ mode: "matrix" }) }), 0)).toBe(
      "route.live.saveFirst",
    );
  });
});

describe("withLevel", () => {
  it("changes one field of one track and keeps the rest of the row", () => {
    const next = withLevel(profile(), 1, { gain: 0.5 });
    expect(next.tracks[1]).toEqual({ track: 1, enabled: true, gain: 0.5, muted: true });
    expect(next.tracks[0]).toEqual({ track: 0, enabled: true, gain: 1 });
  });

  it("leaves the draft alone for a track it does not hold", () => {
    const p = profile();
    expect(withLevel(p, 5, { muted: true })).toBe(p);
  });
});

describe("levelOutcome", () => {
  it("a plain live apply is quiet", () => {
    expect(levelOutcome({ applied: "live" })).toMatchObject({
      tone: "quiet",
      key: "route.live.appliedLive",
    });
  });

  it("a live apply that changed nothing still says why", () => {
    expect(levelOutcome({ applied: "live", reason: "no change to apply" })).toMatchObject({
      tone: "quiet",
      key: "route.live.appliedLiveNote",
      reason: "no change to apply",
    });
  });

  it("a restart is visible and carries its reason", () => {
    expect(levelOutcome({ applied: "restart", reason: "not running" })).toMatchObject({
      tone: "warn",
      key: "route.live.appliedRestart",
      reason: "not running",
    });
    expect(levelOutcome({ applied: "restart" }).key).toBe("route.live.appliedRestartBare");
  });

  it("a stored change whose reconcile failed is not quiet", () => {
    expect(levelOutcome({ applied: "live", warnings: ["reconcile failed"] }).tone).toBe("warn");
  });
});

describe("carryMutes", () => {
  it("keeps a mute across a preset for a track the preset also holds", () => {
    const next = profile({
      tracks: [
        { track: 1, enabled: true, gain: 1 },
        { track: 2, enabled: true, gain: 1 },
      ],
    });
    const out = carryMutes(next, profile());
    expect(out.tracks).toEqual([
      { track: 1, enabled: true, gain: 1, muted: true },
      { track: 2, enabled: true, gain: 1 },
    ]);
  });

  it("returns the preset untouched when nothing was muted", () => {
    const next = profile();
    expect(carryMutes(next, profile({ tracks: [{ track: 1, enabled: true, gain: 1 }] }))).toBe(next);
  });
});
