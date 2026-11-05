/* What a routing Save will do to a running destination, before it is saved.
 *
 * THE SERVER'S RULE, NOT AN APPROXIMATION OF IT. The engine restarts a
 * destination when its argv signature moves (engine destSpec over
 * destArgvSig), and that signature masks the level of every named volume stage
 * (ffmpeg.MaskLiveVolumes): a change of level alone goes to the running FFmpeg
 * on its command channel. Of what a routing save can change, the argv reads
 * exactly these (engine destSpecFor): the compile's filterComplex, outLabel,
 * secondOutLabel, tracks and videoDelayMs, and the profile's sampleRate. So the
 * same comparison, over the stored compile and the draft's, is the answer.
 *
 * It used to compare whole profiles as well, as a safety net. That net said
 * "restart" for a change the engine never restarts for: switching music rights
 * on for a destination the music track had already left changes the profile and
 * nothing it sends (the demo's YouTube, E3). videoDelayMs is what the net was
 * there for, and it is compared directly now.
 *
 * Three answers, and a fourth for "not known yet". While the CURRENT draft's
 * compile is still out (every tick of a fader drag starts a new one) the page
 * does not know, and says so rather than defaulting to "restart" (E1: "Saving
 * restarts Kick" flashing amber mid-drag on a level change). Neither a gain-only
 * profile diff nor the previous answer can stand in for it: a fader crossing the
 * limiter threshold flips the normaliser, which is structural (routing
 * filtergraph.go), and only the compile of this draft says whether it did.
 *
 * The page cannot see the server's platform: on Windows the live path is not
 * verified and every level change restarts. So the level-only answer is worded
 * "where supported", never as a promise; the server's answer after the save is
 * what the toast reports. */

/** The spelling routing emits for a named volume stage, the same pattern
 *  internal/ffmpeg/live_volume.go's liveVolumeRE recognises. */
const LIVE_VOLUME = /volume@([A-Za-z0-9_]+)=volume=([0-9.]+)/g;

/** The graph with every named volume level blanked, as the server masks it. */
export function maskLiveVolumes(graph: string): string {
  return graph.replace(LIVE_VOLUME, "volume@$1=volume=*");
}

/** The parts of a routing compile (routing.Result) that reach the argv. */
export interface ArgvRouting {
  filterComplex: string;
  outLabel: string;
  secondOutLabel?: string;
  tracks: number[] | null;
  videoDelayMs?: number;
}

/** restart: the argv moves, so saving restarts the destination.
 *  levels: only named volume levels move, which the server applies live.
 *  none: nothing the destination sends changes, so it keeps running untouched.
 *  pending: the current draft's compile has not answered yet. */
export type SaveImpact = "restart" | "levels" | "none" | "pending";

/** The argv-relevant half of one compile, as one comparable string. Absent,
 *  null and zero read alike, as they do on the server's side of the wire. */
function argvKey(r: ArgvRouting, sampleRate: number | null | undefined): string {
  return JSON.stringify([
    maskLiveVolumes(r.filterComplex),
    r.outLabel,
    r.secondOutLabel ?? "",
    r.tracks ?? [],
    r.videoDelayMs ?? 0,
    sampleRate ?? 0,
  ]);
}

export function saveImpact(input: {
  /** The compile of the stored profile. */
  stored: ArgvRouting | null | undefined;
  /** The compile of the CURRENT draft: undefined while it is still out, null
   *  when it failed. A compile of an older draft must not be passed. */
  draft: ArgvRouting | null | undefined;
  storedSampleRate?: number | null;
  draftSampleRate?: number | null;
  /** A changed second (VOD) mix is a second graph this page does not compare,
   *  so it counts as a restart. */
  secondMixChanged: boolean;
}): SaveImpact {
  if (input.secondMixChanged) return "restart";
  if (input.draft === undefined) return "pending";
  const { stored, draft } = input;
  // A compile that failed, or a stored one the list did not carry: the
  // conservative answer. A failed draft compile blocks Save anyway.
  if (!stored || !draft) return "restart";
  if (argvKey(stored, input.storedSampleRate) !== argvKey(draft, input.draftSampleRate)) return "restart";
  return stored.filterComplex === draft.filterComplex ? "none" : "levels";
}

/** How a destination save took effect, read back by the server after its
 *  reconcile (PUT /destinations/{id}'s `applied`): "live" when the running
 *  FFmpeg kept running, "restart" when this save replaced it. Absent when
 *  nothing was running to apply it to, or when the reconcile failed. */
export type SaveApplied = "live" | "restart";

/** The toast after a routing save, from the SERVER's answer rather than the
 *  prediction above: E3's "is restarting with the new mix" came from the page's
 *  own guess on a save the engine applied without restarting anything.
 *
 *  `levelsMoved` is whether the saved draft moved a track's gain or mute, which
 *  is what "the level change" in route.savedLive refers to. With no answer the
 *  toast claims nothing about a restart: "Routing saved." is true either way. */
export function savedToastKey(input: {
  enabled: boolean;
  applied?: string;
  levelsMoved: boolean;
}): "route.saved" | "route.savedRestarting" | "route.savedLive" | "route.savedKeptRunning" {
  if (!input.enabled) return "route.saved";
  if (input.applied === "restart") return "route.savedRestarting";
  if (input.applied === "live") return input.levelsMoved ? "route.savedLive" : "route.savedKeptRunning";
  return "route.saved";
}
