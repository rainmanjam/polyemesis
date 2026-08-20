/* How a batch of uploads finished, and what the operator is told about it.
 *
 * Three defects in one loop, all of them about the same thing: the loop had ONE
 * `error` string and no idea what an abort was.
 *
 *   - A DELIBERATE CANCEL WAS PAINTED AS A FAILURE. `xhr.onabort` rejects with
 *     ApiError(0, "upload cancelled"), the loop caught it like any other
 *     rejection, and it landed in the red border-destructive banner. Pressing
 *     the Cancel button and being shown an error is the app disagreeing with
 *     the operator about what just happened -- and it trains them to read that
 *     banner as noise, which is the banner that has to work when a real upload
 *     fails.
 *
 *   - THREE OF FIVE FAILURES WERE SILENT. `setError(...)` in the catch, once
 *     per file, so the last rejection overwrote every earlier one. Drop five
 *     files, have three refused, and the operator is shown one filename and
 *     believes the other two are on the server. They find out when the
 *     broadcast they scheduled goes to air -- which is exactly the failure the
 *     comment above that catch says it exists to prevent.
 *
 * Kept out of the component and made a plain function so it can be tested as a
 * decision, which is this repo's way of testing something that would otherwise
 * only be reachable through a drag-and-drop event. */

/** What became of one file in a batch. */
export type UploadResult =
  | { name: string; kind: "ok" }
  | { name: string; kind: "cancelled" }
  | { name: string; kind: "failed"; detail: string };

/** Classify one finished upload.
 *
 *  `aborted` is read from the AbortController's own signal rather than from the
 *  rejection's message. The message is a string the API client happens to write
 *  today; the signal is the fact. */
export function classifyUpload(
  name: string,
  aborted: boolean,
  detail: string,
): UploadResult {
  if (aborted) return { name, kind: "cancelled" };
  return { name, kind: "failed", detail };
}

/** Every failure in the batch, one line each, in the order they happened.
 *
 *  A LIST, not a string: the single `error` slot is what made three of five
 *  refusals invisible, and no amount of wording fixes a container that holds
 *  one thing. */
export function failureLines(results: readonly UploadResult[]): string[] {
  return results
    .filter((r): r is Extract<UploadResult, { kind: "failed" }> => r.kind === "failed")
    .map((r) => `${r.name}: ${r.detail}`);
}

/** The files the operator cancelled. Never a failure — they asked for this. */
export function cancelledNames(results: readonly UploadResult[]): string[] {
  return results.filter((r) => r.kind === "cancelled").map((r) => r.name);
}
