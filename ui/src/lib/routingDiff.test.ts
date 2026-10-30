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
