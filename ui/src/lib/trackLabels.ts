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
