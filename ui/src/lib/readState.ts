/* One read, three states, kept apart so a page cannot render one as another.
 *
 * The bug this exists to make unwriteable is `.catch(() => {})` followed by an
 * empty state. Three separate places did it:
 *
 *   - SettingsPage's ApiTokens swallowed a failed GET /tokens and rendered
 *     "No tokens yet." to somebody auditing who can administer the box.
 *   - PlatformSettings swallowed three reads, so `creds: null` drew an
 *     unconfigured install over a configured one and the operator regenerated
 *     a client secret that was working.
 *   - AutomationPage swallowed one Promise.all and left four empty lists.
 *
 * In every case a failed read was STORED AS THE SAME VALUE as a successful
 * empty one, and the empty state is a positive claim: it says the server
 * answered and there is nothing there. A question that was never answered has
 * no business producing an answer.
 *
 * This is a CONTROL rather than a warning: `mayClaim` is a type guard, so the
 * only way to reach the value at all is down the branch where the read
 * actually succeeded. There is no expression in which a page can print an
 * empty state for a read that failed, because there is no value to print it
 * beside. */

/** The state of one read. `pending` and `failed` are deliberately distinct:
 *  the first says "wait", the second says "this is as much as you will get". */
export type ReadState<T> =
  | { kind: "pending" }
  | { kind: "failed" }
  | { kind: "ok"; value: T };

export function pendingRead<T>(): ReadState<T> {
  return { kind: "pending" };
}

export function failedRead<T>(): ReadState<T> {
  return { kind: "failed" };
}

export function okRead<T>(value: T): ReadState<T> {
  return { kind: "ok", value };
}

/** Whether the page may make a POSITIVE CLAIM about what came back -- "No
 *  tokens yet.", "Sent 0", "not configured". True only when the server
 *  actually answered.
 *
 *  A type guard, so the value is unreachable outside that branch. */
export function mayClaim<T>(s: ReadState<T>): s is { kind: "ok"; value: T } {
  return s.kind === "ok";
}

/** The rows to render, and nothing else. An unanswered read contributes no
 *  rows -- but see `mayClaim` before printing "there are none". */
export function rowsOf<T>(s: ReadState<T[]>): T[] {
  return s.kind === "ok" ? s.value : [];
}

/** Whether the failure notice should be on screen. */
export function readFailed<T>(s: ReadState<T>): boolean {
  return s.kind === "failed";
}
