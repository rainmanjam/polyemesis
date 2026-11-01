import type { DestStatus, DestinationId } from "@/lib/types";

/** The 9:16 tiers a YouTube vertical half may pick (#7). The keys are the
 *  server's: internal/db/youtube_vertical_tiers.go names them, and
 *  youtubeVertical.test.ts reads that file so this list cannot drift from it. */
export const YOUTUBE_VERTICAL_TIERS = ["vertical1080p30", "vertical720p30"] as const;
export type YouTubeVerticalTier = (typeof YOUTUBE_VERTICAL_TIERS)[number];

/** How often the vertical half's YouTube health is read while a half is live (#7).
 *  The server enforces the same floor, so a faster client only gets the cached answer.
 *  YouTube's correction 6 sets the floor: liveStreams.list is paced against the daily
 *  quota, and a 10 s poll would spend about 8,640 units a day on one stream. */
export const YOUTUBE_VERTICAL_POLL_MS = 60_000;

/** The rows the dashboard draws as cards: every destination except a vertical
 *  half, which is drawn as its horizontal card's sub-row (#7). The full list is
 *  still what the server is told about on reorder, so callers that persist order
 *  must read the full list, not this one. */
export function primaryDestinations(dests: DestStatus[]): DestStatus[] {
  return dests.filter((d) => d.verticalHalfOf == null);
}

/** The vertical row a horizontal card carries, or undefined when it has none, or
 *  when the row is not in this list (a stale link never draws a phantom half). */
export function verticalHalfOf(dest: DestStatus, dests: DestStatus[]): DestStatus | undefined {
  const id: DestinationId | undefined = dest.youtubeVertical?.verticalId;
  if (id == null) return undefined;
  return dests.find((d) => d.id === id);
}

/** How many horizontal pairs still lack the one-time Live Control Room step.
 *  The go-live sheet reads this to remind the operator until it is confirmed. */
export function unconfirmedVerticalPairs(dests: DestStatus[]): number {
  return dests.filter((d) => d.youtubeVertical != null && !d.youtubeVertical.pairingConfirmed).length;
}
