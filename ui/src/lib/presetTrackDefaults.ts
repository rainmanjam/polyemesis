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

/** One programme's layout as the role defaults read it: what the operator said
 *  each track is, and which tracks the ingest has. */
export interface RoleLayout {
  sourceId: number;
  annotations: readonly TrackAnnotation[];
  tracks: readonly number[];
}

/** The track indices of a programme's measured layout, or the six-stereo
 *  placeholder useSourceTracks shows before the ingest is probed -- the same
 *  pickers, offered the same options, whichever programme they describe. */
export function layoutTrackIndices(tracks: readonly { index: number }[] | null | undefined): number[] {
  if (tracks && tracks.length > 0) return tracks.map((tr) => tr.index);
  return [0, 1, 2, 3, 4, 5];
}

/** Whose annotations and tracks the role defaults come from: the EDITED
 *  DESTINATION'S programme, never the one selected in the header.
 *
 *  A preset picks track indices and the server applies it against the
 *  destination's programme (applyPreset is sent its source id, #497). Reading
 *  the roles from the header's programme instead opened the music picker on
 *  another ingest's music track and applied the preset with it -- on a
 *  two-programme install, Studio B's mic went out as its music bed.
 *
 *  `local` is what the page holds and the operator may be typing into, so it
 *  wins while it describes that programme; otherwise `read`, that programme's
 *  own layout from GET /source. Until one of them describes it there are no
 *  role defaults at all -- the server's generic slots stand, which is a guess
 *  rather than another programme's answer. A legacy destination with no
 *  programme keeps the page's layout, as everything else on the page does. */
export function roleLayoutFor(
  editedSourceId: number | null,
  local: RoleLayout | null,
  read: RoleLayout | null,
): Pick<RoleLayout, "annotations" | "tracks"> {
  if (editedSourceId === null) return local ?? { annotations: [], tracks: [] };
  if (local?.sourceId === editedSourceId) return local;
  if (read?.sourceId === editedSourceId) return read;
  return { annotations: [], tracks: [] };
}
