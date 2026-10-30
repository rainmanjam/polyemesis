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
}): boolean {
  const { storedGraph, draftGraph, secondMixChanged } = input;
  if (secondMixChanged || !storedGraph || !draftGraph) return false;
  return maskLiveVolumes(storedGraph) === maskLiveVolumes(draftGraph);
}
