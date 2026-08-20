import type { TrackAnnotation } from "./types";

/** The duck a fresh "on" would create, or null when none can be built.
 *
 *  THE GUARD THIS REPLACES WAS WRONG ABOUT THE FEATURE. The Ducking card
 *  returned null whenever fewer than two tracks were in the destination's mix,
 *  on the reasoning that a duck needs "something to push down and something to
 *  push it down with", both from the mix. The compiler says otherwise:
 *  duckGraph() in internal/routing/filtergraph.go explicitly keeps a trigger
 *  that is NOT in the mix -- it taps the ingest track straight into the
 *  detector -- because "that is how a feed which excludes the mic can still
 *  duck its music when the host speaks". Which is the ordinary case, not an
 *  edge one, and the card's own enable() has always seeded triggers from the
 *  ingest's whole track list rather than from the mix.
 *
 *  So excluding the mic role from a destination made the entire Ducking card
 *  vanish -- switch included -- while the stored duck kept compiling and kept
 *  pulling the music down, with nothing on screen to turn it off.
 *
 *  What a duck really needs is one target IN THE MIX (there is nothing to push
 *  down otherwise) and one trigger PRESENT ON THE INGEST and distinct from the
 *  targets. That is what this returns, and null is the honest "cannot be
 *  created", which is a claim about creation only: a duck that already exists
 *  is rendered regardless.
 *
 *  A CONTROL: the switch is offered only where turning it on produces a graph
 *  that does something, so the operator cannot arm a duck that silently does
 *  nothing.
 */
export interface DuckSeed {
  trigger: number[];
  target: number[];
}

export function duckSeed(
  mixedTracks: number[],
  allTracks: number[],
  annotations: TrackAnnotation[],
): DuckSeed | null {
  if (mixedTracks.length === 0) return null;

  // Seed from the roles when they exist: mic ducks music is the case this
  // feature was built for, and it is one click when we already know which is
  // which.
  const beds = annotations
    .filter((a) => a.role === "music" || a.role === "game")
    .map((a) => a.track)
    .filter((t) => mixedTracks.includes(t));
  const mics = annotations
    .filter((a) => a.role === "mic" || a.role === "commentary")
    .map((a) => a.track)
    .filter((t) => allTracks.includes(t));

  const target = beds.length ? beds : [mixedTracks[0]];
  // Trigger and target must be disjoint -- the compiler refuses a track that is
  // both -- so a role-seeded trigger is filtered the same way a hand-picked one
  // would be, and the fallback is the first ingest track that is not a target.
  const roleTrigger = mics.filter((t) => !target.includes(t));
  const trigger = roleTrigger.length
    ? roleTrigger
    : allTracks.filter((t) => !target.includes(t)).slice(0, 1);

  if (trigger.length === 0) return null;
  return { trigger, target };
}
