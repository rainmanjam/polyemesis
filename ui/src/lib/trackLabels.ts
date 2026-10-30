import type { SourceInfo } from "@/lib/types";

/** What the operator has called each ingest track, indexed by track index.
 *
 *  THE SAME PRECEDENCE THE ROUTING EDITOR USES. TrackRows.tsx resolves a
 *  track's name as `ann.label || t.title` -- the operator's own annotation
 *  first, the container's embedded title second -- and a second rule here
 *  would mean a track called "Guest mic" in the editor and something else in
 *  the tooltip that claims to describe it.
 *
 *  NULL WHEN THE SNAPSHOT DESCRIBES A DIFFERENT PROGRAMME, and that is the
 *  whole reason this is a function rather than two lines inlined at the call
 *  site. The dashboard's `source` is scoped to the selected programme, but on
 *  a multi-programme install it draws destination cards for EVERY programme.
 *  Labelling Studio B's destination with Studio A's track names would put a
 *  confident, specific, wrong answer under the operator's cursor -- worse than
 *  the unnamed tooltip it replaced, because a reader has no way to tell it is
 *  about the wrong show. Returning null falls back to the plain wording.
 */
export function trackLabels(
  source: Pick<SourceInfo, "id" | "tracks" | "annotations"> | null | undefined,
  sourceId: number | null | undefined,
): (string | undefined)[] | null {
  if (!source || sourceId == null || source.id !== sourceId) return null;

  const byIndex = new Map<number, string>();
  for (const t of source.tracks ?? []) {
    const title = t.title?.trim();
    if (title) byIndex.set(t.index, title);
  }
  // Annotations last, so they win. Note this loop can only ADD a name or
  // replace one -- an annotation with an empty label leaves the container's
  // title in place rather than blanking it, which is what an operator who
  // cleared the field would expect to see.
  for (const a of source.annotations ?? []) {
    const label = a.label?.trim();
    if (label) byIndex.set(a.track, label);
  }
  if (byIndex.size === 0) return null;

  const max = Math.max(...byIndex.keys());
  return Array.from({ length: max + 1 }, (_, i) => byIndex.get(i));
}

/** The tooltip on one track chip.
 *
 *  Reads as a sentence either way, because a tooltip that changes shape
 *  depending on whether a name exists is harder to scan than one that does
 *  not. "Track 3 (Commentary) is included" and "Track 3 is included" differ
 *  only by the part that is genuinely new.
 */
export function trackChipTitle(index: number, included: boolean, label?: string): string {
  const named = label ? `Track ${index + 1} (${label})` : `Track ${index + 1}`;
  return `${named} is ${included ? "included" : "excluded"}`;
}

/** How many audio tracks this card's programme's ingest carries, or null when
 *  that is not known here: the snapshot is another programme's (the same rule
 *  as trackLabels above), or the probe has not answered. */
export function trackCount(
  source: Pick<SourceInfo, "id" | "probed" | "tracks"> | null | undefined,
  sourceId: number | null | undefined,
): number | null {
  if (!source || sourceId == null || source.id !== sourceId) return null;
  if (!source.probed) return null;
  return source.tracks?.length ?? 0;
}

/** How many track chips TrackSummary draws.
 *
 *  It drew SIX, always -- a constant from before the layout was probed -- so a
 *  four-track ingest showed chips 5 and 6 as "excluded" tracks that do not
 *  exist. One chip per ingest track when the layout is known; when it is not,
 *  only as far as the highest routed track, inventing nothing. A routed track
 *  past the end of the layout is still drawn, because a route to a track the
 *  ingest no longer has is exactly what the operator needs to see. */
export function trackChipCount(count: number | null | undefined, included: readonly number[]): number {
  const routed = included.length > 0 ? Math.max(...included) + 1 : 0;
  return Math.max(count ?? 0, routed);
}
