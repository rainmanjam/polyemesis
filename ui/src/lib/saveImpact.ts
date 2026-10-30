/* What a routing Save will do to a running destination.
 *
 * Since I4 a destination's restart signature is taken over its graph with the
 * level of every named volume stage masked (ffmpeg.MaskLiveVolumes). A Save
 * whose compiled graph differs from the stored one ONLY in those levels (a
 * gain or a mute) is applied to the running process on its command channel and
 * does not restart it. Anything else, a track added, a matrix cell, a limiter
 * that a gain pushed in, still restarts.
 *
 * The page cannot see the server's platform: on Windows the live path is not
 * verified and every level change restarts. So the level-only answer is worded
 * "where supported", never as a promise. */

/** The spelling routing emits for a named volume stage, the same pattern
 *  internal/ffmpeg/live_volume.go's liveVolumeRE recognises. */
const LIVE_VOLUME = /volume@([A-Za-z0-9_]+)=volume=([0-9.]+)/g;

/** The graph with every named volume level blanked, as the server masks it. */
export function maskLiveVolumes(graph: string): string {
  return graph.replace(LIVE_VOLUME, "volume@$1=volume=*");
}

/** True when saving the draft changes nothing but live volume levels.
 *
 *  `draftGraph` must be the compile of the CURRENT draft; a caller holding an
 *  older compile passes null and gets false, the conservative answer. A changed
 *  second (VOD) mix counts as structural: it is a second graph this page does
 *  not compare. */
export function isLevelOnlySave(input: {
  storedGraph: string | null | undefined;
  draftGraph: string | null;
  secondMixChanged: boolean;
  /** Both profiles, so a change that reaches the command line OUTSIDE the
   *  audio graph is seen too: a negative delay holds the video back in the
   *  output arguments and leaves the graph identical. */
  storedProfile?: unknown;
  draftProfile?: unknown;
}): boolean {
  const { storedGraph, draftGraph, secondMixChanged } = input;
  if (secondMixChanged || !storedGraph || !draftGraph) return false;
  if (maskLiveVolumes(storedGraph) !== maskLiveVolumes(draftGraph)) return false;
  return withoutLevels(input.storedProfile) === withoutLevels(input.draftProfile);
}

/** A profile as a canonical string with every track's gain and mute removed.
 *  Absent, null and empty values read alike, so a draft that only spells
 *  "nothing" differently is not a change; a difference this misses can only
 *  make the answer "restart", the safe direction. */
function withoutLevels(profile: unknown): string {
  const canon = (v: unknown, key?: string): unknown => {
    if (v === null || v === undefined) return undefined;
    if (Array.isArray(v)) {
      if (v.length === 0) return undefined;
      const items = v.map((x) => canon(x, key));
      return key === "tracks"
        ? [...items].sort(
            (a, b) =>
              ((a as { track?: number })?.track ?? 0) - ((b as { track?: number })?.track ?? 0),
          )
        : items;
    }
    if (typeof v === "object") {
      const out: Record<string, unknown> = {};
      for (const k of Object.keys(v as object).sort()) {
        if (key === "tracks" && (k === "gain" || k === "muted")) continue;
        // `key` names the array an object sits in, so a track row (inside
        // "tracks") is the one whose gain and mute are dropped above.
        const c = canon((v as Record<string, unknown>)[k], k);
        if (c !== undefined) out[k] = c;
      }
      return out;
    }
    return v;
  };
  return JSON.stringify(canon(profile) ?? null);
}
