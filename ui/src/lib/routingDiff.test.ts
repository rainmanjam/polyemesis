import { describe, expect, it } from "vitest";

import { changedTracks, routingChanges } from "./routingDiff";
import type { RoutingProfile } from "./types";

const base: RoutingProfile = {
  mode: "simple",
  tracks: [
    { track: 0, enabled: true, gain: 1 },
    { track: 1, enabled: true, gain: 1 },
    { track: 2, enabled: false, gain: 1 },
  ],
  matrix: null,
  normalize: "auto",
  sampleRate: 48000,
};

describe("routingChanges (A7)", () => {
  it("is empty when the draft is the stored profile", () => {
    expect(routingChanges({ stored: base, draft: structuredClone(base) })).toEqual([]);
  });

  it("names a track leaving and a track joining the mix", () => {
    const draft = structuredClone(base);
    draft.tracks[1].enabled = false;
    draft.tracks[2].enabled = true;
    expect(routingChanges({ stored: base, draft })).toEqual([
      { kind: "trackRemoved", track: 1 },
      { kind: "trackAdded", track: 2 },
    ]);
  });

  it("reports gain and mute as a level change, not a removal", () => {
    const draft = structuredClone(base);
    draft.tracks[0].gain = 0.5;
    draft.tracks[1].muted = true;
    expect(routingChanges({ stored: base, draft })).toEqual([
      { kind: "trackLevel", track: 0 },
      { kind: "trackLevel", track: 1 },
    ]);
  });

  it("reports settings by name and treats an empty list like none", () => {
    const draft = { ...structuredClone(base), excludeRoles: [], delayMs: 120, ducking: { trigger: [0], target: [1] } };
    const kinds = routingChanges({ stored: base, draft, storedSecond: null, draftSecond: base }).map((c) => c.kind);
    expect(kinds).toEqual(["delay", "ducking", "secondMix"]);
  });

  it("says nothing without a draft", () => {
    expect(routingChanges({ stored: base, draft: null })).toEqual([]);
    expect(routingChanges({ stored: base, draft: undefined })).toEqual([]);
  });

  it("treats a new destination's draft against defaults: mode, normaliser and empty tracks", () => {
    // No stored profile: simple mode, auto normaliser, no rows. A draft that
    // matches those defaults changes nothing but the tracks it turns on, and
    // a sample rate is not reported for a profile that never existed.
    const draft: RoutingProfile = { ...structuredClone(base), tracks: [{ track: 0, enabled: true }] };
    expect(routingChanges({ stored: null, draft })).toEqual([{ kind: "trackAdded", track: 0 }]);
    expect(routingChanges({ stored: undefined, draft: { ...draft, mode: "matrix" } }).map((c) => c.kind)).toEqual([
      "trackAdded",
      "mode",
    ]);
  });

  it("does not report a level change on a track that stays out of the mix", () => {
    const draft = structuredClone(base);
    draft.tracks[2].gain = 0.25;
    draft.tracks[2].muted = true;
    expect(routingChanges({ stored: base, draft })).toEqual([]);
  });

  it("reads a missing gain as unity and a missing mute as unmuted", () => {
    const stored: RoutingProfile = { ...structuredClone(base), tracks: [{ track: 0, enabled: true }] };
    const same: RoutingProfile = { ...structuredClone(base), tracks: [{ track: 0, enabled: true, gain: 1, muted: false }] };
    expect(routingChanges({ stored, draft: same })).toEqual([]);
    const quieter: RoutingProfile = { ...structuredClone(base), tracks: [{ track: 0, enabled: true, gain: 0.5 }] };
    expect(routingChanges({ stored, draft: quieter })).toEqual([{ kind: "trackLevel", track: 0 }]);
  });

  it("reports the loudness normaliser and its target", () => {
    const off = { ...structuredClone(base), normalize: "off" } as RoutingProfile;
    expect(routingChanges({ stored: base, draft: off }).map((c) => c.kind)).toEqual(["loudness"]);
    const target = { ...structuredClone(base), loudness: { targetLufs: -14 } } as unknown as RoutingProfile;
    expect(routingChanges({ stored: base, draft: target }).map((c) => c.kind)).toEqual(["loudness"]);
  });

  it("reports a music-rights change, but not an empty list against none", () => {
    const music = { ...structuredClone(base), excludeRoles: ["music"] } as RoutingProfile;
    expect(routingChanges({ stored: base, draft: music }).map((c) => c.kind)).toEqual(["music"]);
    expect(routingChanges({ stored: music, draft: { ...structuredClone(base), excludeRoles: [] } }).map((c) => c.kind)).toEqual([
      "music",
    ]);
    expect(routingChanges({ stored: { ...structuredClone(base), excludeRoles: [] }, draft: base })).toEqual([]);
  });

  it("collects the touched tracks once, in order", () => {
    expect(
      changedTracks([
        { kind: "trackLevel", track: 2 },
        { kind: "matrix" },
        { kind: "trackRemoved", track: 0 },
        { kind: "trackLevel", track: 2 },
      ]),
    ).toEqual([0, 2]);
  });
});
