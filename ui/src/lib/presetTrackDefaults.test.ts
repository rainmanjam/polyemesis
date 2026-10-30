/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import {
  layoutTrackIndices,
  presetOptsFromRoles,
  presetPicks,
  roleLayoutFor,
  type RoleLayout,
} from "./presetTrackDefaults";
import type { PresetOpts, TrackAnnotation } from "./types";

/* The routing presets' track pickers default from what the operator said each
 * track IS, not from the server's generic slot numbers. The demo's Studio A is
 * mic, commentary, music, other -- music is Track 3 (index 2) -- and the music
 * picker used to open on Track 1 because the server's default musicTrack is 0. */

const SERVER: PresetOpts = {
  musicTrack: 0,
  micTrack: 2,
  surroundTrack: 0,
  cleanTrack: 1,
  language: "",
  musicPolicy: "",
};

// scripts/demo-seed.fixture.json, Studio A.
const STUDIO_A: TrackAnnotation[] = [
  { track: 0, role: "mic", label: "Host mic" },
  { track: 1, role: "commentary", label: "Co-host mic" },
  { track: 2, role: "music", label: "Music bed (licensed)" },
  { track: 3, role: "other", label: "Crowd / room" },
];
const FOUR = [0, 1, 2, 3];

describe("presetOptsFromRoles", () => {
  it("defaults the music picker to the track annotated music", () => {
    const opts = presetOptsFromRoles(SERVER, STUDIO_A, FOUR, {});
    expect(opts.musicTrack).toBe(2); // "Track 3"
  });

  it("defaults mic and clean from their roles too", () => {
    const anns: TrackAnnotation[] = [
      { track: 3, role: "mic" },
      { track: 1, role: "clean" },
    ];
    const opts = presetOptsFromRoles(SERVER, anns, FOUR, {});
    expect(opts.micTrack).toBe(3);
    expect(opts.cleanTrack).toBe(1);
  });

  it("takes the mic from a commentary track when nothing is annotated mic", () => {
    const opts = presetOptsFromRoles(SERVER, [{ track: 3, role: "commentary" }], FOUR, {});
    expect(opts.micTrack).toBe(3);
  });

  it("prefers a mic over a commentary track, and the lowest index on a tie", () => {
    const anns: TrackAnnotation[] = [
      { track: 1, role: "commentary" },
      { track: 3, role: "mic" },
      { track: 2, role: "mic" },
    ];
    expect(presetOptsFromRoles(SERVER, anns, FOUR, {}).micTrack).toBe(2);
  });

  it("falls back to the server's default when no track has the role", () => {
    const opts = presetOptsFromRoles(SERVER, [], FOUR, {});
    expect(opts).toEqual(SERVER);
  });

  it("ignores a role on a track the ingest does not have", () => {
    // The Select would render a blank value for a track it has no option for.
    const opts = presetOptsFromRoles(SERVER, [{ track: 7, role: "music" }], FOUR, {});
    expect(opts.musicTrack).toBe(SERVER.musicTrack);
  });

  it("never overrides the operator's own pick", () => {
    const opts = presetOptsFromRoles(SERVER, STUDIO_A, FOUR, { musicTrack: 0 });
    expect(opts.musicTrack).toBe(0);
    // ...and only the field they picked: the others still follow the roles.
    expect(opts.micTrack).toBe(0);
  });
});

describe("presetPicks", () => {
  it("records only the fields an edit changed", () => {
    const shown = presetOptsFromRoles(SERVER, STUDIO_A, FOUR, {});
    expect(presetPicks(shown, { ...shown, musicTrack: 3 })).toEqual({ musicTrack: 3 });
    expect(presetPicks(shown, { ...shown })).toEqual({});
  });
});

/* AND THE PAGE ROUTES THROUGH IT. A unit test of the function passes just as
 * happily with the page still handing PresetRow the raw server defaults. */
const ROOT = new URL("../../../", import.meta.url).pathname;
const routing = () => readFileSync(join(ROOT, "ui/src/pages/RoutingPage.tsx"), "utf8");

describe("RoutingPage, wired to the role defaults", () => {
  it("hands the pickers and the apply call the role-derived options", () => {
    const src = routing();
    // The roles are the EDITED DESTINATION'S programme's, not the header's.
    expect(src).toMatch(/roleLayoutFor\(editedSourceId,/);
    expect(src).toMatch(/presetOptsFromRoles\(presetOpts, roles\.annotations, roles\.tracks,/);
    expect(src).toContain("api.applyPreset(presetId, shownPresetOpts, editedSourceId)");
    expect(src).toContain("presetOpts: shownPresetOpts,");
    expect(src).toContain("setPresetOpts: pickPresetOpts,");
  });
});

/* Whose roles the pickers open on. The edited destination's programme, never
 * the header's: a preset is applied against the destination's programme. */
describe("roleLayoutFor", () => {
  const studioA: RoleLayout = { sourceId: 1, annotations: [{ track: 1, role: "music" }], tracks: [0, 1, 2, 3] };
  const studioB: RoleLayout = { sourceId: 2, annotations: [{ track: 3, role: "music" }], tracks: [0, 1, 2, 3] };

  it("reads the destination's own programme when the page holds another", () => {
    expect(roleLayoutFor(2, studioA, studioB)).toBe(studioB);
  });

  it("prefers the page's layout when it describes the destination's programme", () => {
    const typed: RoleLayout = { ...studioB, annotations: [{ track: 2, role: "music" }] };
    expect(roleLayoutFor(2, typed, studioB)).toBe(typed);
  });

  it("has no roles at all, rather than another programme's, until that layout is read", () => {
    expect(roleLayoutFor(2, studioA, null)).toEqual({ annotations: [], tracks: [] });
    expect(roleLayoutFor(2, studioA, studioA)).toEqual({ annotations: [], tracks: [] });
  });

  it("keeps the page's layout for a legacy destination with no programme", () => {
    expect(roleLayoutFor(null, studioA, studioB)).toBe(studioA);
    expect(roleLayoutFor(null, null, studioB)).toEqual({ annotations: [], tracks: [] });
  });
});

describe("layoutTrackIndices", () => {
  it("lists a measured layout's indices and the six-track placeholder before a probe", () => {
    expect(layoutTrackIndices([{ index: 0 }, { index: 2 }])).toEqual([0, 2]);
    expect(layoutTrackIndices(null)).toEqual([0, 1, 2, 3, 4, 5]);
    expect(layoutTrackIndices([])).toEqual([0, 1, 2, 3, 4, 5]);
  });
});
