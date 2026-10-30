import type { AttentionItem, OnAir } from "@/lib/attention";

/** The one-line answer to "is every destination healthy, and what needs me?".
 *
 *  DERIVED FROM THE SAME INPUTS AS THE LIST BELOW IT -- the onAir() counts and
 *  the attention() rows -- so the verdict and the list cannot disagree. A
 *  verdict computed from a separate status read could say "all healthy" over a
 *  row that is plainly red, and an operator who believes the line stops reading
 *  the rows. */
export type HealthVerdict =
  | { kind: "healthy"; count: number; uptimeSec: number | null }
  | { kind: "attention"; count: number; total: number; firstDestinationId: string };

export function healthVerdict(
  air: OnAir,
  items: readonly AttentionItem[],
  ingestUptimeSec: number | null,
): HealthVerdict | null {
  if (air.enabled === 0) return null;

  const destItems = items.filter((i) => i.destinationId);
  if (destItems.length > 0) {
    // Counted per destination, not per row: one destination with two problems
    // is one destination that needs attention.
    const ids = new Set(destItems.map((i) => i.destinationId as string));
    return {
      kind: "attention",
      count: ids.size,
      total: air.enabled,
      firstDestinationId: destItems[0].destinationId as string,
    };
  }

  // A problem with no destination (the ingest, the renditions) has no card to
  // name and no count to give. The list under the strip says what it is; a
  // "healthy" line over it would contradict it.
  if (items.length > 0) return null;

  // "All healthy" needs every enabled destination live. A quietly stopped one
  // with no error is not healthy, and neither is one still connecting.
  if (air.live > 0 && air.live === air.enabled) {
    return { kind: "healthy", count: air.live, uptimeSec: ingestUptimeSec };
  }
  return null;
}
