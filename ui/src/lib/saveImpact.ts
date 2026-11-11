/* What a routing Save will do to a running destination, before it is saved.
 *
 * THE ENGINE'S RULE, over what this page can see. The engine restarts a
 * destination when its argv signature moves (engine destSpec over
 * destArgvSig), and that signature masks the level of every named volume stage
 * (ffmpeg.MaskLiveVolumes): a change of level alone goes to the running FFmpeg
 * on its command channel. Of what a routing save can change, the argv reads
 * exactly these (engine.DestSpecFor): the compile's filterComplex, outLabel,
 * secondOutLabel, tracks and videoDelayMs, and the profile's sampleRate -- and
 * on an audio-copy destination only the tracks, since its argv forwards them
 * untouched and never takes the graph (ffmpeg copyAudioArgs). So the same
 * comparison, over the stored compile and the draft's, is the answer for a
 * destination's live mix.
 *
 * Where the page cannot see the engine's inputs it answers "restart", the
 * cautious direction for a warning shown before an on-air save, and the toast
 * after the save reports what really happened (PUT's `applied`). Two such
 * places: a change to the second (VOD) mix, whose stored compile the page does
 * not have (the engine compiles the pair; a VOD gain alone is in fact live),
 * and a programme not yet probed, which the engine plans with a provisional
 * compile this page's compiles do not use.
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

import { WIRE, type Member } from "@/lib/contract/wire";

/** The spelling routing emits for a named volume stage, the same pattern
 *  internal/ffmpeg/live_volume.go's liveVolumeRE recognises. */
const LIVE_VOLUME = /volume@(\w+)=volume=([0-9.]+)/g;

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
  /** An audio-copy destination (audio.copy on any kind but "audio"): its argv
   *  takes the track list and nothing else of the routing, and has no volume
   *  stage for a level to reach. */
  copyAudio?: boolean;
}): SaveImpact {
  if (input.secondMixChanged) return "restart";
  if (input.draft === undefined) return "pending";
  const { stored, draft } = input;
  // A compile that failed, or a stored one the list did not carry: the
  // conservative answer. A failed draft compile blocks Save anyway.
  if (!stored || !draft) return "restart";
  if (input.copyAudio) {
    return JSON.stringify(stored.tracks ?? []) === JSON.stringify(draft.tracks ?? []) ? "none" : "restart";
  }
  if (argvKey(stored, input.storedSampleRate) !== argvKey(draft, input.draftSampleRate)) return "restart";
  return stored.filterComplex === draft.filterComplex ? "none" : "levels";
}

/** How a destination save took effect, read back by the server after its
 *  reconcile (PUT /destinations/{id}'s `applied`, Engine.AppliedSince): "live"
 *  when the running FFmpeg kept running, "restart" when this save replaced it.
 *  Absent when nothing was running to apply it to, or when the reconcile failed. */
export type SaveApplied = Member<typeof WIRE.appliedStates>;

/** The toast after a routing save, from the SERVER's answer rather than the
 *  prediction above: E3's "is restarting with the new mix" came from the page's
 *  own guess on a save the engine applied without restarting anything.
 *
 *  A "live" answer with no `reason` is a level the running process took on its
 *  command channel; any reason means it kept running without one (nothing it
 *  sends changed, a copy destination stored a level that reaches nothing, or
 *  only the backup feed moved).
 *  The profile cannot tell these apart: a gain moved on a muted track changes
 *  the profile and nothing the process plays. With no answer the toast claims
 *  nothing about a restart: "Routing saved." is true either way. */
export function savedToastKey(input: {
  enabled: boolean;
  applied?: string;
  reason?: string;
}): "route.saved" | "route.savedRestarting" | "route.savedLive" | "route.savedKeptRunning" {
  if (!input.enabled) return "route.saved";
  if (input.applied === "restart") return "route.savedRestarting";
  if (input.applied === "live") return input.reason ? "route.savedKeptRunning" : "route.savedLive";
  return "route.saved";
}
