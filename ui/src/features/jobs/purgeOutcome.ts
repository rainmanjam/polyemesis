/* How the result of a purge is reported.
 *
 * "Nothing was old enough to purge" used to be raised as `new Error(...)` so
 * that it would land in the shared `act()` helper's catch and reach the
 * screen. It reached the screen in the RED toast reserved for things that went
 * wrong -- an operator who asked a question and got the honest answer "none"
 * was told their action failed.
 *
 * A control is not available here: the operator asked, and the answer is
 * legitimately zero. So the fix is to stop miscolouring the answer, and to
 * stop using an exception as a return channel -- an Error thrown on a
 * successful call is a lie to every caller between here and the catch. */

export type ToastTone = "info" | "success";

/** A purge that matched nothing is an ANSWER, not a failure. */
export function purgeTone(purged: number): ToastTone {
  return purged === 0 ? "info" : "success";
}
