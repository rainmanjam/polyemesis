import { describe, expect, it } from "vitest";
import {
  carryMutes,
  destinationLiveBlock,
  setMuted,
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
  it("a plain destination can take a level", () => {
    expect(destinationLiveBlock(dest())).toBeNull();
  });

  it("an audio-copy destination cannot, as the route's 409 says", () => {
    const d = dest({ kind: "srt", audio: { copy: true } });
    expect(destinationLiveBlock(d)).toBe("route.live.copyBlocked");
  });

  it("an Icecast row encodes, so copy does not block it", () => {
    const d = dest({ kind: "audio" as Destination["kind"], audio: { copy: true } });
    expect(destinationLiveBlock(d)).toBeNull();
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

describe("setMuted", () => {
  it("sets the mute on a held track and keeps the rest of its row", () => {
    expect(setMuted(profile().tracks, 0, true)[0]).toEqual({ track: 0, enabled: true, gain: 1, muted: true });
  });

  it("adds a track the selection lacks as disabled, so the cells still decide routing", () => {
    expect(setMuted(profile().tracks, 3, true)[2]).toEqual({ track: 3, enabled: false, gain: 1, muted: true });
  });
});
