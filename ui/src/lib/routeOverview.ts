/* The Overview grid's model: destinations down, tracks across, one cell per
 * pair. Everything here is read off the same stored or draft routing the Simple
 * and Mix views edit, so the grid can never say something the editor does not.
 *
 * A VOD-bound destination has a second mix (the VOD profile). A music-role track
 * that is in that mix, on a destination that does not keep music out, is the
 * risk the operator needs to see: it reaches the archive. */

/** The tracks a mix sends, read off the mode it is in: a matrix mix sends a track
 *  when any of its cells has gain, a simple mix when the track is enabled. */
export function routedTracks(p: {
  mode: "simple" | "matrix";
  tracks: { track: number; enabled: boolean }[];
  matrix: { track: number; gain: number }[] | null;
}): number[] {
  if (p.mode === "matrix") {
    const cells = p.matrix ?? [];
    return [...new Set(cells.filter((c) => c.gain > 0).map((c) => c.track))].sort((a, b) => a - b);
  }
  return p.tracks.filter((s) => s.enabled).map((s) => s.track);
}

/** One click in the grid: flip the track in this mix's selection. An absent track
 *  is added enabled at unity, the same default the Simple rows write. */
export function toggleTrackSelection(
  cur: { track: number; enabled: boolean; gain: number }[],
  track: number,
): { track: number; enabled: boolean; gain: number }[] {
  if (!cur.some((x) => x.track === track)) return [...cur, { track, enabled: true, gain: 1 }];
  return cur.map((x) => (x.track === track ? { ...x, enabled: !x.enabled } : x));
}

export interface OverviewDest {
  id: number;
  name: string;
  enabled: boolean;
  /** Tracks sent in the live mix. */
  routed: number[];
  /** Whether a second, VOD mix exists for this destination. */
  vodBound: boolean;
  /** Tracks sent in the VOD mix. Empty unless `vodBound`. */
  vodRouted: number[];
  /** The LIVE mix's "keep music out" guard. */
  guardedMusic: boolean;
  /** The VOD mix's own "keep music out" guard. Each mix has its own; the VOD output
   *  is compiled from the VOD profile, so this is the one that decides the archive. */
  vodGuardedMusic: boolean;
  state?: string | null;
}

export type CellState = "off" | "routed" | "musicVod";
export type MusicRights = "blocked" | "reachesVod" | "none";

export function cellState(dest: OverviewDest, track: number, music: number[]): CellState {
  const isMusic = music.includes(track);
  if (isMusic && dest.vodBound && !dest.vodGuardedMusic && dest.vodRouted.includes(track)) {
    return "musicVod";
  }
  return dest.routed.includes(track) ? "routed" : "off";
}

export function musicRights(dest: OverviewDest, music: number[]): MusicRights {
  // The archive is the risk, so it is checked first: a live guard does not hide a
  // music track that is still in the VOD mix.
  if (dest.vodBound && !dest.vodGuardedMusic && music.some((t) => dest.vodRouted.includes(t))) {
    return "reachesVod";
  }
  if (dest.guardedMusic || (dest.vodBound && dest.vodGuardedMusic)) return "blocked";
  return "none";
}

export interface MusicVodHit {
  destId: number;
  track: number;
}

/** Every (destination, music track) pair where the music reaches a VOD mix and
 *  the destination does not keep music out. This is the list the fix strip names. */
export function musicReachingVod(dests: OverviewDest[], music: number[]): MusicVodHit[] {
  const hits: MusicVodHit[] = [];
  for (const d of dests) {
    if (!d.vodBound || d.vodGuardedMusic) continue;
    for (const t of music) {
      if (d.vodRouted.includes(t)) hits.push({ destId: d.id, track: t });
    }
  }
  return hits;
}
