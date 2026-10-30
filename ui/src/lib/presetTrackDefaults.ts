import type { PresetOpts, TrackAnnotation, TrackRole } from "./types";

/* Which track each routing-preset picker opens on.
 *
 * The server's /routing/presets `defaults` are generic slot numbers (music 0,
 * mic 2, clean 1) that know nothing about this ingest. The operator has
 * already said what each track IS in the track annotations, so a picker whose
 * role has an annotated track opens on that track -- on the demo's Studio A the
 * music picker said "Track 1" over the mic while the music bed sat on Track 3.
 *
 * Kept as a derivation rather than written into state, because the presets and
 * the annotations arrive from two different reads in either order; deriving
 * means neither order can overwrite the other. */

/** The roles that answer each picker, in preference order. Surround has no
 *  role in the catalogue, so it keeps the server's default. */
const ROLES_FOR: Partial<Record<keyof PresetOpts, readonly TrackRole[]>> = {
  musicTrack: ["music"],
  micTrack: ["mic", "commentary"],
  cleanTrack: ["clean"],
};

function trackWithRole(
  annotations: readonly TrackAnnotation[],
  tracks: ReadonlySet<number>,
  roles: readonly TrackRole[],
): number | undefined {
  for (const role of roles) {
    const matches = annotations
      .filter((a) => a.role === role && tracks.has(a.track))
      .map((a) => a.track)
      .sort((x, y) => x - y);
    if (matches.length > 0) return matches[0];
  }
  return undefined;
}

/** The options the pickers show and a preset is applied with.
 *
 *  `base` is the server's defaults; a role-annotated track replaces a default
 *  (lowest index on a tie; an annotation on a track the ingest does not have
 *  is ignored, since the picker has no option to show for it); `picks` -- the
 *  fields the operator chose by hand -- win over both, so a later annotation
 *  or a re-read of the defaults never moves a picker the operator set. */
export function presetOptsFromRoles(
  base: PresetOpts,
  annotations: readonly TrackAnnotation[],
  tracks: readonly number[],
  picks: Partial<PresetOpts>,
): PresetOpts {
  const present = new Set(tracks);
  const fromRoles: Partial<PresetOpts> = {};
  for (const [field, roles] of Object.entries(ROLES_FOR) as [keyof PresetOpts, readonly TrackRole[]][]) {
    const track = trackWithRole(annotations, present, roles);
    if (track !== undefined) (fromRoles as Record<string, unknown>)[field] = track;
  }
  return { ...base, ...fromRoles, ...picks };
}

/** The fields an edit changed, relative to what the pickers were showing --
 *  which is what the operator chose and must be remembered as a pick. */
export function presetPicks(shown: PresetOpts, next: PresetOpts): Partial<PresetOpts> {
  const out: Partial<PresetOpts> = {};
  for (const key of Object.keys(next) as (keyof PresetOpts)[]) {
    if (next[key] !== shown[key]) (out as Record<string, unknown>)[key] = next[key];
  }
  return out;
}
