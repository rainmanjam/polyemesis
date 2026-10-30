import type { DestStatus, DestinationId, SourceView } from "@/lib/types";

/* THE GO LIVE SHEET'S ARITHMETIC.
 *
 * The sheet replaces "Start all", which fired every destination at once with no
 * count and no scope. Starting is done one destination at a time, through the
 * same per-destination path the card's Start button uses (applyDestinationEnabled
 * on the server), so it is guard-equivalent to pressing those buttons -- see the
 * header of internal/api/destinations_bulk.go. Nothing here adds a server-side
 * scope to a bulk route: a scoped listing would make out-of-scope broadcasts
 * look deleted (lifecycle.go:800-806).
 */

export type BlockedReason = "noSource";

export interface GoLiveRow {
  id: DestinationId;
  name: string;
  platform: string;
  /** Whether this destination is already on. A live row is shown, never started. */
  enabled: boolean;
  sourceName: string | null;
  ready: boolean;
  blockedReason: BlockedReason | null;
}

/** One row per destination, with readiness decided from what the server knows.
 *
 *  A destination whose programme is not on this install cannot start: the
 *  engine has no source to feed it. That is the one readiness fact the status
 *  row carries honestly. "No stream key" is NOT derived here: the list handler's
 *  handling of the key is not something this sheet can rely on to be empty for
 *  a missing key rather than redacted, so a false "blocked" is not offered. */
export function goLiveRows(
  destinations: readonly DestStatus[],
  sources: readonly SourceView[],
): GoLiveRow[] {
  const byId = new Map(sources.map((s) => [s.id, s.name]));
  return destinations.map((d) => {
    const sourceName = d.sourceId != null ? (byId.get(d.sourceId) ?? null) : null;
    const blocked = sourceName === null;
    return {
      id: d.id,
      name: d.name,
      platform: d.platform,
      enabled: d.enabled,
      sourceName,
      ready: !blocked,
      blockedReason: blocked ? "noSource" : null,
    };
  });
}

/** The destinations a Go live press starts: ready, and not already on. */
export function planStarts(rows: readonly GoLiveRow[]): GoLiveRow[] {
  return rows.filter((r) => r.ready && !r.enabled);
}

/** How many YouTube broadcasts a Stop would end. An enabled YouTube destination
 *  is a broadcast in progress, and stopping it is permanent (see
 *  destinations_bulk.go). Counted so the operator sees it before the click. */
export function youtubeBroadcastsAtRisk(destinations: readonly DestStatus[]): number {
  return destinations.filter((d) => d.enabled && d.platform === "youtube").length;
}

/** Starts the destinations one at a time, `paceMs` apart, collecting a message
 *  per refusal. A refusal does not stop the walk: the rest of a Go live press
 *  should still go out, and the operator is shown which one did not take.
 *
 *  The pace is the server's own bulk pacing (bulkStartPacing, 2s), so a long
 *  list is spread out the same way it was under Start all. */
export async function runPacedStarts(
  ids: readonly number[],
  start: (id: number) => Promise<void>,
  paceMs: number,
): Promise<Map<number, string>> {
  const errors = new Map<number, string>();
  for (let i = 0; i < ids.length; i++) {
    try {
      await start(ids[i]);
    } catch (err) {
      errors.set(ids[i], err instanceof Error ? err.message : String(err));
    }
    if (paceMs > 0 && i < ids.length - 1) {
      await new Promise((r) => setTimeout(r, paceMs));
    }
  }
  return errors;
}
