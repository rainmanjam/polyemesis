import type { RoutingProfile } from "./types";

/* What a routing Save changes, in words an operator can check before applying
 * it to a destination that is on air (A7).
 *
 * The save bar already says WHETHER saving restarts the destination. This says
 * WHAT moves: a track leaving the mix is the change an operator most needs to
 * catch before it reaches the audience, so tracks are named one by one and
 * everything else is reported as the setting that changed.
 *
 * A track is "in" when its row is enabled. Gain and mute are reported as a
 * level change on that track, not as an add or a remove, because a muted track
 * stays in the graph. */

export type RoutingChange =
  | { kind: "trackAdded"; track: number }
  | { kind: "trackRemoved"; track: number }
  | { kind: "trackLevel"; track: number }
  | { kind: "mode" | "matrix" | "loudness" | "delay" | "ducking" | "music" | "sampleRate" | "secondMix" };

const same = (a: unknown, b: unknown) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);

type TrackRow = NonNullable<RoutingProfile["tracks"]>[number];

/** The one change a single track row makes, if any: it joined the mix, left it,
 *  or stayed in and moved its level. */
function trackChange(track: number, was: TrackRow | undefined, now: TrackRow | undefined): RoutingChange | null {
  const wasIn = !!was?.enabled;
  const isIn = !!now?.enabled;
  if (!wasIn && isIn) return { kind: "trackAdded", track };
  if (wasIn && !isIn) return { kind: "trackRemoved", track };
  if (isIn && ((was?.gain ?? 1) !== (now?.gain ?? 1) || !!was?.muted !== !!now?.muted)) {
    return { kind: "trackLevel", track };
  }
  return null;
}

function trackChanges(stored: RoutingProfile | null | undefined, draft: RoutingProfile): RoutingChange[] {
  const rows = (p: RoutingProfile | null | undefined) => new Map((p?.tracks ?? []).map((r) => [r.track, r]));
  const before = rows(stored);
  const after = rows(draft);
  const tracks = [...new Set([...before.keys(), ...after.keys()])].sort((a, b) => a - b);
  const out: RoutingChange[] = [];
  for (const track of tracks) {
    const change = trackChange(track, before.get(track), after.get(track));
    if (change) out.push(change);
  }
  return out;
}

/** The shape of the mix: its mode, its matrix, and how it is loudness-normalised. */
function mixChanges(stored: RoutingProfile | null | undefined, draft: RoutingProfile): RoutingChange[] {
  const out: RoutingChange[] = [];
  if ((stored?.mode ?? "simple") !== draft.mode) out.push({ kind: "mode" });
  if (!same(stored?.matrix?.length ? stored.matrix : null, draft.matrix?.length ? draft.matrix : null)) {
    out.push({ kind: "matrix" });
  }
  if ((stored?.normalize ?? "auto") !== draft.normalize || !same(stored?.loudness, draft.loudness)) {
    out.push({ kind: "loudness" });
  }
  return out;
}

/** The settings that act on the whole output: delay, ducking, music exclusion
 *  and sample rate. */
function outputChanges(stored: RoutingProfile | null | undefined, draft: RoutingProfile): RoutingChange[] {
  const out: RoutingChange[] = [];
  if ((stored?.delayMs ?? 0) !== (draft.delayMs ?? 0)) out.push({ kind: "delay" });
  if (!same(stored?.ducking, draft.ducking)) out.push({ kind: "ducking" });
  if (!same(stored?.excludeRoles?.length ? stored.excludeRoles : null, draft.excludeRoles?.length ? draft.excludeRoles : null)) {
    out.push({ kind: "music" });
  }
  if ((stored?.sampleRate ?? 0) !== draft.sampleRate && stored) out.push({ kind: "sampleRate" });
  return out;
}

export function routingChanges(input: {
  stored: RoutingProfile | null | undefined;
  draft: RoutingProfile | null | undefined;
  storedSecond?: RoutingProfile | null;
  draftSecond?: RoutingProfile | null;
}): RoutingChange[] {
  const { stored, draft } = input;
  if (!draft) return [];
  const out = [...trackChanges(stored, draft), ...mixChanges(stored, draft), ...outputChanges(stored, draft)];
  if (!same(input.storedSecond, input.draftSecond)) out.push({ kind: "secondMix" });
  return out;
}

/** The tracks a change list touches, in order, for the one-line summary. */
export function changedTracks(changes: readonly RoutingChange[]): number[] {
  const set = new Set<number>();
  for (const c of changes) if ("track" in c) set.add(c.track);
  return [...set].sort((a, b) => a - b);
}
