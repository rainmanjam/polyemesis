import type { AttentionItem, OnAir } from "@/lib/attention";
import type { DestinationId } from "@/lib/types";

/** The one-line answer to "is every destination healthy, and what needs me?".
 *
 *  DERIVED FROM THE SAME INPUTS AS THE LIST BELOW IT -- the onAir() counts and
 *  the attention() rows -- so the verdict and the list cannot disagree. A
 *  verdict computed from a separate status read could say "all healthy" over a
 *  row that is plainly red, and an operator who believes the line stops reading
 *  the rows. */
export type HealthVerdict =
  | { kind: "healthy"; count: number; uptimeSec: number | null }
  | { kind: "attention"; count: number; total: number; firstDestinationId: DestinationId }
  /** Every destination is up, and the feed feeding them is not arriving. A
   *  healthy line over that would be the strip's most expensive lie (A3). */
  | { kind: "no-ingest" };

export function healthVerdict(
  air: OnAir,
  items: readonly AttentionItem[],
  ingestUptimeSec: number | null,
  /** True while the broadcast is on its backup, a playlist or a slate. The
   *  attention list does not raise failover at all (its rows are destinations
   *  and the ingest), so without this a slate with every destination running
   *  would read "All healthy" directly above the failover banner. */
  offPrimary = false,
  /** Whether the selected programme's ingest is receiving bytes (the same
   *  signal the header chip reads, which is true for SRT with no ingest process).
   *  Undefined means "not read yet", which must not read as a fault. */
  ingestReceiving?: boolean,
): HealthVerdict | null {
  if (air.enabled === 0) return null;
  // Off the primary is not healthy, whatever the destinations report. The
  // failover banner says what is on air; this line must not contradict it.
  if (offPrimary) return null;

  const destItems = items.filter((i) => i.destinationId);
  if (destItems.length > 0) {
    // Counted per destination, not per row: one destination with two problems
    // is one destination that needs attention.
    const ids = new Set(destItems.map((i) => i.destinationId));
    return {
      kind: "attention",
      count: ids.size,
      total: air.enabled,
      firstDestinationId: destItems[0].destinationId as DestinationId,
    };
  }

  // Every destination up over a feed that is not arriving is not healthy.
  if (ingestReceiving === false && air.live > 0) return { kind: "no-ingest" };

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
