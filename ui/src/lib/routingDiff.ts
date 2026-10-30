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

export function routingChanges(input: {
  stored: RoutingProfile | null | undefined;
  draft: RoutingProfile | null | undefined;
  storedSecond?: RoutingProfile | null;
  draftSecond?: RoutingProfile | null;
}): RoutingChange[] {
  const { stored, draft } = input;
  const out: RoutingChange[] = [];
  if (!draft) return out;

  const rows = (p: RoutingProfile | null | undefined) => new Map((p?.tracks ?? []).map((r) => [r.track, r]));
  const before = rows(stored);
  const after = rows(draft);
  const tracks = [...new Set([...before.keys(), ...after.keys()])].sort((a, b) => a - b);
  for (const track of tracks) {
    const was = before.get(track);
    const now = after.get(track);
    const wasIn = !!was?.enabled;
    const isIn = !!now?.enabled;
    if (!wasIn && isIn) out.push({ kind: "trackAdded", track });
    else if (wasIn && !isIn) out.push({ kind: "trackRemoved", track });
    else if (isIn && ((was?.gain ?? 1) !== (now?.gain ?? 1) || !!was?.muted !== !!now?.muted)) {
      out.push({ kind: "trackLevel", track });
    }
  }

  if ((stored?.mode ?? "simple") !== draft.mode) out.push({ kind: "mode" });
  if (!same(stored?.matrix?.length ? stored.matrix : null, draft.matrix?.length ? draft.matrix : null)) {
    out.push({ kind: "matrix" });
  }
  if ((stored?.normalize ?? "auto") !== draft.normalize || !same(stored?.loudness, draft.loudness)) {
    out.push({ kind: "loudness" });
  }
  if ((stored?.delayMs ?? 0) !== (draft.delayMs ?? 0)) out.push({ kind: "delay" });
  if (!same(stored?.ducking, draft.ducking)) out.push({ kind: "ducking" });
  if (!same(stored?.excludeRoles?.length ? stored.excludeRoles : null, draft.excludeRoles?.length ? draft.excludeRoles : null)) {
    out.push({ kind: "music" });
  }
  if ((stored?.sampleRate ?? 0) !== draft.sampleRate && stored) out.push({ kind: "sampleRate" });
  if (!same(input.storedSecond, input.draftSecond)) out.push({ kind: "secondMix" });
  return out;
}

/** The tracks a change list touches, in order, for the one-line summary. */
export function changedTracks(changes: readonly RoutingChange[]): number[] {
  const set = new Set<number>();
  for (const c of changes) if ("track" in c) set.add(c.track);
  return [...set].sort((a, b) => a - b);
}
