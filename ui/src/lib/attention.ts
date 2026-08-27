import type { DestinationId, DestStatus, ProcessState, RenditionStatus, Status } from "./types";
import type { SignalTone } from "./signal";

/* What the dashboard says FIRST, as plain functions.
 *
 * The dashboard renders every fact it has at one level: the preview, the ingest
 * card, the pipeline rows, the composer and every destination card, all equally
 * loud. That is a reasonable inventory and a poor answer to the two questions an
 * operator actually asks under load — "am I on air?" and "what is broken?" —
 * because both answers are somewhere in it rather than at the top of it.
 *
 * These functions are the top of it. They live in lib rather than in the page
 * for the reason dashboardFacts.ts gives: deciding that a stopped destination is
 * not a fault, and that a reconnecting one is, is a judgement with a right and a
 * wrong answer, and a judgement worth arguing about is worth testing without a
 * browser.
 */

/** The one-line answer to "am I on air?".
 *
 *  `enabled` rather than `total` is what `live` is counted against everywhere
 *  it is shown. A destination the operator switched off is not a destination
 *  that failed to go live, and counting it in the denominator makes a correctly
 *  configured install read as permanently short of target — which is how an
 *  operator learns to stop reading the number.
 */
export interface OnAir {
  /** Destinations whose process is running. */
  live: number;
  /** Destinations the operator has asked for. The denominator. */
  enabled: number;
  /** Enabled destinations that are trying and not there: starting, or
   *  reconnecting after a drop. */
  degraded: number;
  /** Enabled destinations whose process failed. */
  failed: number;
}

export function onAir(destinations: readonly DestStatus[] | null | undefined): OnAir {
  const rows = (destinations ?? []).filter((d) => d.enabled);
  const count = (...states: ProcessState[]) =>
    rows.filter((d) => d.process?.state && states.includes(d.process.state)).length;
  return {
    live: count("running"),
    enabled: rows.length,
    degraded: count("starting", "reconnecting"),
    failed: count("failed"),
  };
}

/** One thing that is wrong, in the operator's own vocabulary.
 *
 *  `detail` is the SERVER's words and is never invented here. A row that has no
 *  detail says so by omitting it; a row that fabricates a plausible reason sends
 *  someone to look in the wrong place, which is worse than a row that only names
 *  the subject.
 */
export interface AttentionItem {
  /** Stable across status frames, so React keeps the row rather than
   *  re-mounting it every two seconds while the operator is reading it. */
  key: string;
  tone: SignalTone;
  /** What is wrong, by the name the operator gave it. */
  subject: string;
  /** The state that makes it wrong, when there is one. Left as the raw state so
   *  the component can localise it through the same useStateLabel() every other
   *  status on the page goes through — an English string baked in here would be
   *  the one status in the console that never translates. */
  state?: ProcessState;
  detail?: string;
  /** The destination this row is about, when it is about one. The panel is a
   *  summary, and a summary whose rows cannot be followed to the thing they
   *  describe is a second place to read the same fault rather than a way in. */
  destinationId?: DestinationId;
}

/** Worst first. `down` outranks `warn` because a failed destination is off air
 *  and a reconnecting one may not be, and an operator who reads only the first
 *  row must read the one that is costing them viewers. */
const TONE_RANK: Record<string, number> = { down: 0, warn: 1 };

/** Everything on this install that is not as the operator asked for it.
 *
 *  DELIBERATELY NOT EVERY IMPERFECTION. A stopped destination, an idle recorder
 *  and a rendition nobody enabled are all states somebody chose, and listing
 *  them under "needs attention" is how a panel like this becomes something an
 *  operator scrolls past. Only three things qualify: a process that failed, a
 *  process that is trying and has not got there, and a stop the server could not
 *  confirm.
 */
export function attention(status: Status | null | undefined): AttentionItem[] {
  if (!status) return [];
  const items: AttentionItem[] = [];

  // The ingest first, and it is first in the list as well as in the code: every
  // destination below it is carrying the same feed, so an ingest that is down
  // explains the whole panel rather than being one more row in it.
  const ingest = status.ingest;
  if (ingest && ingest.state !== "running" && ingest.state !== "stopped") {
    items.push({
      key: "ingest",
      tone: ingest.state === "failed" ? "down" : "warn",
      subject: "Ingest",
      state: ingest.state,
      detail: ingest.lastError,
    });
  }

  for (const d of status.destinations ?? []) {
    if (!d.enabled) continue;
    const state = d.process?.state;
    if (state === "failed") {
      items.push({
        key: `dest-${d.id}`,
        tone: "down",
        subject: d.name,
        state,
        detail: d.process?.lastError ?? d.error,
        destinationId: d.id,
      });
    } else if (state === "reconnecting" || state === "starting") {
      items.push({
        key: `dest-${d.id}`,
        tone: "warn",
        subject: d.name,
        state,
        detail: d.process?.lastError,
        destinationId: d.id,
      });
    }
    // A stop that was never confirmed is its own row, not a footnote on the one
    // above: the child may still be running and still publishing, and the state
    // reads "stopped" on both arms of Stop, so nothing else on the page can say
    // it. See DestStatus.stopWarning.
    if (d.stopWarning) {
      items.push({
        key: `stop-${d.id}`,
        tone: "warn",
        subject: d.name,
        detail: d.stopWarning,
        destinationId: d.id,
      });
    }
  }

  // A rendition only matters here while something is drawing on it. One that
  // failed with no consumers costs nobody a broadcast, and putting it in front
  // of an operator mid-show is asking them to triage a spare part.
  for (const r of status.renditions ?? []) {
    if (r.consumers <= 0) continue;
    if (r.process?.state === "failed" || r.error) {
      items.push({
        key: `rendition-${r.id}`,
        tone: "down",
        subject: renditionSubject(r),
        state: r.process?.state,
        detail: r.error ?? r.process?.lastError,
      });
    }
  }

  return items.sort((a, b) => (TONE_RANK[a.tone] ?? 9) - (TONE_RANK[b.tone] ?? 9));
}

/** Names the rendition as the encode tier it is, so a row reading "720p" is not
 *  mistaken for a destination with the same name. */
function renditionSubject(r: RenditionStatus): string {
  return `${r.name} (rendition)`;
}
