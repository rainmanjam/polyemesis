/** Which action a destination card's main button performs. */
export type DestAction = "start" | "stop";

export function actionFor(enabled: boolean): DestAction {
  return enabled ? "stop" : "start";
}

/** How long after the button's action flips a click is refused.
 *
 *  THE BUTTON SWAPS IN PLACE, and the status socket repaints it roughly every
 *  two seconds. So an operator can read "Start", have the card repaint under
 *  their cursor, and land the click on "Stop" -- ending a live broadcast they
 *  were trying to begin. Nothing about the click looks wrong afterwards: the
 *  server was asked to stop a destination that was running, and it did.
 *
 *  700ms is chosen against human reaction time rather than against the repaint
 *  interval. A pointer already travelling toward a button cannot be redirected
 *  in much under 200ms, and the gap between deciding to click and the click
 *  landing is longer than that again. A guard shorter than the flight time
 *  would not cover the window that matters; a much longer one would start
 *  refusing deliberate clicks that merely followed a legitimate state change.
 */
export const FLIP_GUARD_MS = 700;

/** Whether a click should be honoured, given how long the button has shown
 *  this action.
 *
 *  Refusing is the safe direction here and the asymmetry is deliberate: a
 *  refused click costs one more click, and an honoured one can end a
 *  broadcast that cannot be resumed.
 */
export function clickIsTrustworthy(msSinceFlip: number): boolean {
  return msSinceFlip >= FLIP_GUARD_MS;
}

/** How long a stop is held before it is sent, so it can be taken back.
 *
 *  UNDO RATHER THAN A CONFIRMATION DIALOG. Stopping one destination is
 *  something an operator does often and on purpose, and a dialog in front of a
 *  frequent deliberate action is trained away within a day -- after which it
 *  costs a click and protects nothing. A held action with a visible way out
 *  protects the case the dialog was for (the click nobody meant) without
 *  taxing the case it was not (the hundreds they did).
 *
 *  Five seconds is long enough to notice a card saying "Stopping" and reach
 *  the undo, and short enough that an operator who meant it is not left
 *  wondering whether the click registered.
 */
export const STOP_UNDO_MS = 5_000;

/** Seconds remaining on a held stop, for the label. Rounded UP so the last
 *  partial second still reads "1" rather than "0" while undo is still live --
 *  a countdown showing 0 next to a working button says the opposite of what is
 *  true. */
export function undoSecondsLeft(msRemaining: number): number {
  return Math.max(0, Math.ceil(msRemaining / 1000));
}
