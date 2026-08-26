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

/** The ingest bitrate the card should print, or null for "—".
 *
 * `ingest.progress.bitrateKbps` is the INGEST PROCESS's own number, and for SRT
 * there is no ingest process: engine.reconcileIngest returns early on purpose,
 * because srtserver delivers datagrams straight into the hub and a second thing
 * on that socket would crash-loop. So `ingest` is null on a HEALTHY SRT install
 * and the card printed "—" forever — the most prominent number on the dashboard,
 * permanently blank on the primary operated ingest path. Same root as the
 * "Offline" badge in #514, which was fixed while this was left behind.
 *
 * The fallback is not a new measurement or an estimate. It is the same relay
 * byte series useIngestLive already trusts to decide whether a broadcast is
 * going out at all, so a card that shows a number here is showing the bytes
 * that made it say "Live" one line above.
 *
 * Order matters: the process is preferred where it exists, because on an RTMP
 * or pull ingest that number is measured at the ingest itself, while the relay
 * series is measured one hop later.
 *
 * Returns null rather than 0 when nothing is arriving. A printed 0 is a claim
 * that the stream is running and empty; "—" is the truth, which is that there
 * is nothing to report. */
export function ingestBitrateKbps(
  processState: string | undefined,
  processKbps: number | undefined,
  recent: { kbps: number }[],
  live: boolean,
): number | null {
  if (processState === "running") return processKbps ?? 0;
  if (!live) return null;
  const last = recent.at(-1);
  return last ? last.kbps : null;
}

/** Fold an incoming status snapshot into the one the app keeps.
 *
 * The status socket is INSTALL-WIDE and every engine publishes onto it, so the
 * app receives one programme's snapshot at a time and keeps the last. For most
 * of the payload that is a known wart -- see ingestAttribution, which exists to
 * NAME whose figures are on screen rather than to fix it.
 *
 * For the destination list it is not survivable. That list is the whole install
 * on one page, grouped by programme, so last-writer-wins meant a three-programme
 * box rendered a heading for each and the destinations of whichever engine spoke
 * most recently: 9, then 2, then 2, every two seconds. It looked stable in a
 * screenshot and wrong in a different way each time it was taken.
 *
 * This was hidden until Engine.Status was scoped to its own source (#515). The
 * default engine's snapshot used to carry every row on the machine, so whichever
 * engine won the race published the same list.
 *
 * Fixing the PUBLISHER is the wrong side: publishStatus fires per process
 * transition and is deliberately synchronous, because coalescing it handed a
 * destination a backwards decode timestamp at a failover switch in 3 runs out of
 * 10 -- and a platform drops the connection on a backwards DTS. Asking every
 * engine for its status on that path would multiply a database read per
 * transition to buy something the consumer can do for free.
 *
 * So: a snapshot replaces the destinations of the programme it DESCRIBES, named
 * by source.id, and leaves every other programme's alone. Keyed on the snapshot
 * rather than on the rows it carries, because an engine with no destinations
 * sends an empty list -- and inferring the programme from the rows would then
 * have no way to clear the last ones it had.
 *
 * AND by id as well, because not every snapshot carries only what it names. Two
 * shapes arrive on this socket: api.Server.statusPayload sends the whole
 * install, while Engine.publishStatus sends one programme. Both label
 * themselves with a single source.id, so folding on the label alone made the
 * install-wide one keep the other programmes' rows AND re-add them -- 13
 * destinations rendered as 17. Dropping any row the incoming snapshot already
 * carries makes the fold correct for both shapes rather than only the one it
 * was written against. */
export function mergeStatusDestinations(
  prev: { source?: { id?: number }; destinations?: DestStatusLike[] } | null,
  next: { source?: { id?: number }; destinations?: DestStatusLike[] },
): DestStatusLike[] {
  const incoming = next.destinations ?? [];
  const describes = next.source?.id;
  if (describes === undefined) return incoming;
  const arriving = new Set(incoming.map((d) => d.id));
  const kept = (prev?.destinations ?? []).filter(
    (d) => d.sourceId !== describes && !arriving.has(d.id),
  );
  return [...kept, ...incoming];
}

/** The shape mergeStatusDestinations needs. Deliberately minimal: this fold is
 *  about which programme a row belongs to and nothing else. */
export interface DestStatusLike {
  id: number;
  sourceId?: number | null;
}
