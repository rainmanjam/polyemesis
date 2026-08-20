import type { DestStatus } from "./types";

/** What deleting a rendition takes with it, and how sure we are.
 *
 *  Deleting a rendition never deletes a destination: they fall back to
 *  PASSTHROUGH and are handed the source unchanged, mid-broadcast, at whatever
 *  bitrate and resolution the encoder is sending. That is the situation the
 *  rendition existed to avoid, so the dialog names it before the button.
 *
 *  The bug this exists to prevent is the third state. The live list of users
 *  comes off the status socket, and before the first snapshot lands -- or with
 *  the socket down, or on the two seconds when another programme's engine was
 *  the last writer -- there is no list. The dialogs were handed `[]` for that,
 *  branched on `users.length === 0`, and told the operator "Nothing selects this
 *  rendition, so deleting it changes no destination." Three enabled
 *  destinations then dropped to passthrough on air.
 *
 *  `null` means NOT KNOWN and must never be collapsed into "none". The REST
 *  counts on RenditionView are the fallback: they are a row count from the
 *  database rather than a live list, so they can name a number without naming a
 *  destination -- which is exactly the claim that is true in that state.
 *  RenditionCard has always done this (view.destinations at its Feeds header);
 *  the dialogs simply never did.
 */
export type DeleteConsequence =
  /** The live list arrived and is empty. The only state in which "deleting this
   *  changes no destination" is a true sentence. */
  | { kind: "none" }
  /** The live list arrived. Every affected destination can be named. */
  | { kind: "named"; users: DestStatus[]; enabled: number }
  /** No live list, but the database's own counts. A number, no names. */
  | { kind: "counted"; total: number; enabled: number }
  /** Neither. Nothing is known and nothing may be claimed. */
  | { kind: "unknown" };

export function deleteConsequence(
  users: DestStatus[] | null | undefined,
  counts: { destinations: number; enabledDestinations: number } | null | undefined,
): DeleteConsequence {
  if (users) {
    if (users.length === 0) return { kind: "none" };
    return { kind: "named", users, enabled: users.filter((d) => d.enabled).length };
  }
  if (!counts) return { kind: "unknown" };
  // The REST counts are a row count out of the database rather than a live
  // list, so a zero here IS a reliable "nothing selects it" -- unlike a socket
  // that has not spoken yet, which is an absence of evidence.
  if (counts.destinations === 0) return { kind: "none" };
  return { kind: "counted", total: counts.destinations, enabled: counts.enabledDestinations };
}
