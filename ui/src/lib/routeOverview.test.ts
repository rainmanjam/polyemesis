import { describe, expect, it } from "vitest";
import {
  cellState,
  musicRights,
  musicReachingVod,
  routedTracks,
  toggleTrackSelection,
  type OverviewDest,
} from "./routeOverview";

/* A4: the Overview grid answers one question across every output: is the music
 * track off every VOD-bound destination? These rules decide the amber cell and
 * the music-rights state that the grid shows, so they are pinned here rather
 * than inferred from pixels. */

const base: OverviewDest = {
  id: 1,
  name: "YouTube — full mix",
  enabled: true,
  state: "running",
  routed: [0, 1],
  vodBound: false,
  vodRouted: [],
  guardedMusic: false,
  vodGuardedMusic: false,
};

const MUSIC = [1];

describe("cellState", () => {
  it("marks a routed track as routed and an unrouted one as off", () => {
    expect(cellState(base, 0, MUSIC)).toBe("routed");
    expect(cellState(base, 3, MUSIC)).toBe("off");
  });

  it("marks a music track in a VOD-bound mix amber, not merely routed", () => {
    const vod: OverviewDest = { ...base, vodBound: true, vodRouted: [1] };
    expect(cellState(vod, 1, MUSIC)).toBe("musicVod");
  });

  it("does not mark amber when the destination is not VOD-bound", () => {
    const live: OverviewDest = { ...base, routed: [1], vodBound: false, vodRouted: [1] };
    expect(cellState(live, 1, MUSIC)).toBe("routed");
  });

  it("does not mark amber when the VOD mix keeps music out", () => {
    const guarded: OverviewDest = {
      ...base,
      routed: [],
      vodBound: true,
      vodRouted: [1],
      vodGuardedMusic: true,
    };
    expect(cellState(guarded, 1, MUSIC)).toBe("off");
  });

  it("still marks amber when only the LIVE mix keeps music out", () => {
    // The live guard does not protect the archive: the VOD mix is its own profile.
    const liveGuarded: OverviewDest = {
      ...base,
      routed: [],
      guardedMusic: true,
      vodBound: true,
      vodRouted: [1],
      vodGuardedMusic: false,
    };
    expect(cellState(liveGuarded, 1, MUSIC)).toBe("musicVod");
  });

  it("does not mark a non-music track amber even on a VOD mix", () => {
    const vod: OverviewDest = { ...base, vodBound: true, vodRouted: [0] };
    expect(cellState(vod, 0, MUSIC)).toBe("routed");
  });
});

describe("musicRights", () => {
  it("reads blocked when the live mix keeps music out", () => {
    expect(musicRights({ ...base, guardedMusic: true }, MUSIC)).toBe("blocked");
  });

  it("reads blocked when only the VOD mix keeps music out", () => {
    const vod: OverviewDest = { ...base, vodBound: true, vodRouted: [1], vodGuardedMusic: true };
    expect(musicRights(vod, MUSIC)).toBe("blocked");
  });

  it("reads reachesVod even when the live mix is guarded", () => {
    const risk: OverviewDest = {
      ...base,
      guardedMusic: true,
      vodBound: true,
      vodRouted: [1],
    };
    expect(musicRights(risk, MUSIC)).toBe("reachesVod");
  });

  it("reads reachesVod when a music track is in a VOD-bound mix", () => {
    expect(musicRights({ ...base, vodBound: true, vodRouted: [1] }, MUSIC)).toBe("reachesVod");
  });

  it("reads none for a live-only destination with no music on it", () => {
    expect(musicRights(base, MUSIC)).toBe("none");
  });
});

describe("musicReachingVod", () => {
  it("lists each VOD destination a music track reaches, unguarded only", () => {
    const dests: OverviewDest[] = [
      { ...base, id: 1, vodBound: true, vodRouted: [1] },
      { ...base, id: 2, name: "Archive", vodBound: true, vodRouted: [1], vodGuardedMusic: true },
      { ...base, id: 3, name: "Live only", routed: [1] },
      { ...base, id: 4, name: "Live guarded", guardedMusic: true, vodBound: true, vodRouted: [1] },
    ];
    expect(musicReachingVod(dests, MUSIC).map((h) => [h.destId, h.track])).toEqual([
      [1, 1],
      [4, 1],
    ]);
  });
});

describe("routedTracks", () => {
  it("reads the enabled tracks of a simple-mode mix", () => {
    const p = {
      mode: "simple" as const,
      tracks: [
        { track: 0, enabled: true, gain: 1 },
        { track: 1, enabled: false, gain: 1 },
        { track: 2, enabled: true, gain: 1 },
      ],
      matrix: [],
    };
    expect(routedTracks(p)).toEqual([0, 2]);
  });

  it("reads the tracks with a non-zero cell in a matrix-mode mix", () => {
    const p = {
      mode: "matrix" as const,
      tracks: [{ track: 0, enabled: true, gain: 1 }],
      matrix: [
        { track: 3, channel: 0, out: 0, gain: 0.5 },
        { track: 3, channel: 1, out: 1, gain: 1 },
        { track: 4, channel: 0, out: 0, gain: 0 },
      ],
    };
    expect(routedTracks(p)).toEqual([3]);
  });
});

describe("toggleTrackSelection", () => {
  it("adds an absent track as enabled at unity gain", () => {
    expect(toggleTrackSelection([], 2)).toEqual([{ track: 2, enabled: true, gain: 1 }]);
  });

  it("flips an existing track and keeps its gain", () => {
    const cur = [{ track: 0, enabled: true, gain: 0.5 }];
    expect(toggleTrackSelection(cur, 0)).toEqual([{ track: 0, enabled: false, gain: 0.5 }]);
  });

  it("leaves the other tracks untouched", () => {
    const cur = [
      { track: 0, enabled: true, gain: 1 },
      { track: 1, enabled: false, gain: 1 },
    ];
    expect(toggleTrackSelection(cur, 1)[0]).toEqual(cur[0]);
  });
});
