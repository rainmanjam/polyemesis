import type { TrackAnnotation, TrackRole } from "./types";

/** The ingest tracks a destination leaves out because they are marked as
 *  music: the tracks of ITS OWN programme annotated "music", when its
 *  `excludeRoles` lists "music". Ascending, and empty when the setting is off
 *  or no track is marked.
 *
 *  This mirrors the server: `Profile.ExcludedTracks` in
 *  internal/routing/profile.go (applied by `applyRolePolicy` in
 *  internal/routing/filtergraph.go), restricted to the music role. A track is
 *  excluded on its annotation alone, whether or not the destination routes it,
 *  so "not sent" holds either way. internal/routing/music_exclusion_test.go
 *  runs the server's own policy over the same cases as music-exclusion.test.ts
 *  (fixture: internal/routing/testdata/music-exclusion-cases.json), so the two
 *  cannot drift unseen. Change one side and that fixture tells you. */
export function excludedMusicTracks(
  excludeRoles: readonly TrackRole[],
  annotations: readonly TrackAnnotation[],
): number[] {
  if (!excludeRoles.includes("music")) return [];
  return annotations
    .filter((a) => a.role === "music")
    .map((a) => a.track)
    .sort((a, b) => a - b);
}
