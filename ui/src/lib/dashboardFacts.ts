import type { SourceInfo } from "./types";

/* What the dashboard is allowed to assert, as plain functions.
 *
 * Every decision here has the same shape: the status socket answers a question
 * the operator did not ask, and the page used to print that answer as though it
 * were the one they did. A zero that means "not measured yet", a "disabled" that
 * means "the engine dropped it", a bitrate that belongs to a programme nobody
 * named. They live in lib rather than in the page because each one is a
 * decision with a right and a wrong answer, and a decision worth arguing about
 * is worth testing without a browser. */

/** Who the Ingest card is describing, when that is a real question.
 *
 * The status socket is INSTALL-WIDE. internal/api/previews.go says so in its
 * own words, and ws_policy.go passes TypeStatus through unfiltered: every
 * engine publishes onto one broker and the app keeps one `status`, last writer
 * wins. With two programmes on air the Ingest card's bitrate, uptime, track
 * count and video line alternate between two feeds every two seconds, and
 * nothing on the card says which one is on screen.
 *
 * Scoping the socket is the control and it is not a UI change; until it lands,
 * the honest thing is to NAME the programme these figures came from and say
 * that they follow whoever reported last. That turns an unattributable flicker
 * into a reading with a subject.
 *
 * `null` on a single-programme install, deliberately. There the socket cannot
 * be ambiguous, and a badge repeating the only source's name on every load is
 * noise that teaches the operator to stop reading badges -- which costs them
 * the one install where it means something. Same argument as
 * DestinationCard's source badge.
 */
export function ingestAttribution(
  sourceCount: number | null,
  source: Pick<SourceInfo, "id" | "name"> | null | undefined,
): { name: string } | null {
  if (sourceCount === null || sourceCount <= 1) return null;
  const name = source?.name?.trim();
  if (!name) return null;
  return { name };
}

/** The audio track count, or "not measured".
 *
 * `tracks` is empty until the ingest has been probed, and "Audio tracks 0" is
 * the one reading on this card that gets a stream refused by every major
 * platform -- so an operator who sees it goes and changes their encoder, in the
 * seconds before the probe would have answered. Both neighbouring stats already
 * guard on the ingest actually running; this one printed a number regardless.
 *
 * `probed` is the server's own word for "this layout has been measured", and it
 * has been on SourceInfo unused all along.
 */
export function audioTrackCount(
  source: Pick<SourceInfo, "probed" | "tracks"> | null | undefined,
): number | "—" {
  if (!source?.probed) return "—";
  return source.tracks?.length ?? 0;
}

/** What a pipeline row says when it has no process.
 *
 * "disabled" was printed for every absence, and it is the operator's own
 * setting being read back at them -- so the recorder the storage guard halted,
 * and the meters that simply have not started yet, both report the state the
 * operator explicitly did not choose. It is the worst possible word for it:
 * it says "you turned this off" about a feature that is on.
 *
 * Three answers, because there are three states and the page can tell them
 * apart from settings it already fetches:
 *   disabled - the setting is off. Nothing is meant to be running.
 *   idle     - the setting is on and no process exists. Normal before a stream,
 *              and the shape a halted recorder takes; either way the row is not
 *              claiming the operator asked for this.
 *   unknown  - the settings read has not landed or failed. Say nothing.
 */
export type ProcessAbsence = "disabled" | "idle" | "unknown";

export function processAbsence(featureEnabled: boolean | null | undefined): ProcessAbsence {
  if (featureEnabled === null || featureEnabled === undefined) return "unknown";
  return featureEnabled ? "idle" : "disabled";
}
