/* What the clip buffer card is allowed to assert, and what its switch sends.
 *
 * Both are page decisions with a right and a wrong answer, so they live here
 * and are tested without a browser -- the same shape as dashboardFacts. */

/** The three things a keyframe cell can honestly say. */
export type KeyframeVerdict = "unknown" | "seen" | "none";

/** What the Keyframes stat says, and whether it earns the amber.
 *
 *  The cell used to be `stats?.videoFound ? "seen" : "none"` with the tone
 *  following the same expression, so a buffer that is OFF -- or one whose first
 *  poll has not landed -- was reported in amber as a stream that has been
 *  examined and found to carry no keyframe. That is a claim about a specific
 *  encoder fault, made about a stream nobody looked at, in the colour this
 *  console reserves for "something needs your attention".
 *
 *  Three states, because there are three:
 *    unknown - no buffer stats at all. Say nothing, in muted grey.
 *    seen    - a keyframe has arrived.
 *    none    - stats exist and no keyframe is in them. Amber ONLY while the
 *              buffer is running, because that is the only state in which the
 *              absence is evidence: a stopped buffer holds nothing by design.
 */
export function keyframeVerdict(
  stats: { videoFound: boolean } | null | undefined,
  running: boolean | undefined,
): { verdict: KeyframeVerdict; warn: boolean } {
  if (!stats) return { verdict: "unknown", warn: false };
  if (stats.videoFound) return { verdict: "seen", warn: false };
  return { verdict: "none", warn: running === true };
}

/** The windowSeconds to send with an enable/disable of the buffer.
 *
 *  PUT /clips/buffer documents 0 as "keep the current window", so the page that
 *  only toggles the switch does not have to know what the window is. The switch
 *  sent a literal 0 in BOTH directions, which meant a window the operator had
 *  just typed -- with Apply greyed out precisely because the buffer was still
 *  off -- was discarded by the very click that was supposed to start it, and
 *  then overwritten in the input three seconds later by the poll. No message,
 *  and the number they chose was simply gone.
 *
 *  A CONTROL rather than a warning: the typed window rides along with the
 *  enable, so the sequence that used to lose it now works. Turning the buffer
 *  OFF still sends 0 -- there is nothing to apply a window to, and sending one
 *  would silently commit an edit the operator had not asked to commit.
 */
export function windowOnBufferToggle(enabled: boolean, typedWindowSeconds: number): number {
  if (!enabled) return 0;
  return Number.isFinite(typedWindowSeconds) && typedWindowSeconds > 0 ? typedWindowSeconds : 0;
}
