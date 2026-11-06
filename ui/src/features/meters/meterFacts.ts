/* What the loudness readout is allowed to assert.
 *
 * A page decision extracted to lib, the way this repo tests a decision that
 * lives in a page: the wiring stays one expression at the call site and the
 * argument lives here, testable without a browser. */

/** The two fields that say whether a report is a MEASUREMENT or a placeholder.
 *  Deliberately structural rather than the whole LoudnessReport: the question
 *  is about those two and nothing else. */
export interface LoudnessProgress {
  verdict: "unknown" | "pass" | "warn" | "fail";
  seconds: number;
}

/** Whether anything has actually been measured for this destination.
 *
 *  The analyser publishes a report the moment it is planned, before a single
 *  frame has gone through it: `verdict: "unknown"` with `seconds: 0` and every
 *  float still at its zero value. The page printed those zeros through the same
 *  formatters as a real reading, so a destination nobody has measured read
 *  "Momentary 0.0 LUFS / True peak 0.0 dBTP" -- digital full scale, and 0.0
 *  dBTP is over every ceiling this product ships (the default is -1.0 with a
 *  +1 dB failover), so on a stricter profile the True peak stat was painted
 *  red as well.
 *
 *  A CONTROL, not a warning: there is no honest way to render a number that was
 *  never taken, so the stats are not rendered at all until there is one. The
 *  formatters' own floors (-70 LUFS, -120 dBTP) cannot do this job, because
 *  zero is a plausible value on both scales -- it is the loudest one.
 *
 *  Only the two together. A verdict of unknown with seconds > 0 is a genuine
 *  measurement that has not run long enough to be judged, and the card already
 *  says so in prose; blanking it would throw away the only feedback an operator
 *  has while the integration window fills.
 */
export function loudnessMeasured(r: LoudnessProgress | null | undefined): boolean {
  if (!r) return false;
  return !(r.verdict === "unknown" && r.seconds === 0);
}
