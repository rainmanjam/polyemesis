import type { IngestStreamHealth } from "./types";

/* ===========================================================================
   Turning Facebook's stream_health bag into rows a pane can draw.

   Pure, and in lib/ rather than in the component, so the one rule this feature
   exists to keep can be asserted by a test instead of read off a JSX tree:

     AN ABSENT MEASUREMENT IS ABSENT. IT IS NEVER ZERO.

   A bitrate of 0 on a healthy stream is not a smaller number than the truth,
   it is the opposite of the truth: it says the encoder has stopped. The
   operator's next move after reading it is to go restart something that was
   working. That is the whole cost of `?? 0`.

   The inverse is also enforced here and matters just as much: a number
   Facebook DID send is rendered even when it is zero. A real, reported 0 fps
   on a stalled ingest is the most important number on the pane, and a filter
   that hid zeroes to be safe would delete exactly the reading somebody needs.
   `0` present and `undefined` absent are two states and this module keeps both.
   =========================================================================== */

/** One measurement, ready to draw. A row exists BECAUSE a number arrived; there
 *  is no constructor for an empty one. */
export interface HealthRow {
  /** Facebook's own field name, verbatim.
   *
   *  Not humanised, not mapped onto a label of ours. The spellings are not
   *  established — the LiveVideo node reference that would settle them 404s —
   *  so any mapping table here is a guess, and a guess that misses leaves a
   *  measurement Facebook sent rendered nowhere at all. Showing the raw key
   *  cannot silently drop a field, and an operator comparing this against
   *  Facebook's own dashboard is comparing the same word. */
  name: string;
  value: number;
}

/** The rows for one ingest stream, sorted by name.
 *
 *  Sorted for the reason the Go side sorts `Unparsed`: object key order is not
 *  something to rely on across two reads, and a list that reorders itself
 *  between identical polls reads as a change — which on a health pane refreshing
 *  every two seconds is a flicker that means nothing and looks like everything.
 *
 *  Non-finite values are dropped rather than drawn. NaN and Infinity cannot
 *  come out of the Go side (a non-number lands in `unparsed` there), so this is
 *  a guard against a hand-built payload, not a case seen in practice — but
 *  "NaN" beside a frame rate reads as a fault in the stream rather than as a
 *  fault in the JSON. */
export function healthRows(stream: IngestStreamHealth): HealthRow[] {
  const health = stream.health;
  if (!health) return [];
  return Object.entries(health)
    .filter(([, v]) => typeof v === "number" && Number.isFinite(v))
    .map(([name, value]) => ({ name, value }))
    .sort((a, b) => a.name.localeCompare(b.name));
}

/** Whether this stream carried any measurement at all.
 *
 *  The caller needs this to choose between drawing rows and saying "not
 *  reported" IN WORDS. It must never choose between rows and a row of zeroes. */
export function hasMeasurements(stream: IngestStreamHealth): boolean {
  return healthRows(stream).length > 0;
}

/** A number for display.
 *
 *  No unit is appended and none is inferred. The keys are Facebook's and their
 *  units are not published either, so "1234 kbps" would be a second guess
 *  stacked on the first: if the field turns out to be bits per second, the pane
 *  is off by a thousand and confidently labelled. The number is shown as
 *  Facebook sent it.
 *
 *  Integers stay integers so a frame rate of 30 does not read as "30.00", and
 *  fractions are capped at two places so a float artefact does not push the row
 *  wide mid-poll. */
export function formatHealthValue(value: number): string {
  if (!Number.isFinite(value)) return "";
  if (Number.isInteger(value)) return String(value);

  // A NON-ZERO MEASUREMENT MUST NEVER RENDER AS "0", which is what this did.
  // toFixed(2) turns 0.001 into "0.00" and the trailing-zero strip then turns
  // that into "0" -- so a real, small, arriving-right-now value was displayed
  // as the number that means nothing is arriving. On a stream-health pane that
  // is not a rounding artefact, it is the difference between "your bitrate is
  // tiny" and "your bitrate is zero", and the second one sends an operator to
  // restart an encoder that is working.
  //
  // Same defect class as the viewer count one file over, arriving through
  // arithmetic instead of through a nullish default: absent is not zero, and
  // neither is small.
  const rounded = value.toFixed(2).replace(/\.?0+$/, "");
  if (Number(rounded) === 0) {
    // Sign preserved: a negative that rounds to zero is still not zero, and
    // dropping the sign would be a second small lie on top of the first.
    return value < 0 ? "> -0.01" : "< 0.01";
  }
  return rounded;
}

/** How many ingest streams Facebook is currently describing.
 *
 *  Used for the confirmation's consequences panel, where it is only shown when
 *  it is above zero — see the call site for why a zero there would be read as
 *  "ending this does nothing", which is false. */
export function reportingStreamCount(
  streams: IngestStreamHealth[] | null | undefined,
): number {
  return streams?.length ?? 0;
}
