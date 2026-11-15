/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { excludedMusicTracks } from "./musicExclusion";
import type { TrackAnnotation, TrackRole } from "./types";

/* The console's "which tracks does this destination leave out as music" rule
 * mirrors the server's (Profile.ExcludedTracks). The cases live in one file,
 * internal/routing/testdata/music-exclusion-cases.json, read here and by
 * internal/routing/music_exclusion_test.go, which runs the server's real policy
 * on each. Same pattern as preset-drift.test.ts. */

const ROOT = new URL("../../../", import.meta.url).pathname;

interface Case {
  name: string;
  excludeRoles: TrackRole[];
  tracks: { track: number; role: TrackRole; routed: boolean }[];
  excludedMusicTracks: number[];
  /** The server refuses these annotations outright; nothing to compare. */
  serverRefuses?: boolean;
}

const cases: Case[] = JSON.parse(
  readFileSync(join(ROOT, "internal/routing/testdata/music-exclusion-cases.json"), "utf8"),
).cases;

describe("excludedMusicTracks against the server's cases", () => {
  it("read the fixture", () => {
    expect(cases.length, "fixture parsed to too few cases").toBeGreaterThanOrEqual(10);
  });

  for (const c of cases.filter((x) => !x.serverRefuses)) {
    it(c.name, () => {
      const annotations = c.tracks.map((t) => ({ track: t.track, role: t.role }) as TrackAnnotation);
      expect(excludedMusicTracks(c.excludeRoles, annotations)).toEqual(
        [...c.excludedMusicTracks].sort((a, b) => a - b),
      );
    });
  }
});
