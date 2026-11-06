import { useCallback, useEffect, useRef, useState } from "react";

import {
  FLIP_GUARD_MS,
  STOP_UNDO_MS,
  actionFor,
  clickIsTrustworthy,
  type DestAction,
} from "@/lib/destinationAction";

/** Tracks how long the button has been showing its current action.
 *
 *  The status socket repaints a destination card roughly every two seconds, and
 *  the Start/Stop button swaps IN PLACE on `enabled`. A repaint that lands
 *  between an operator reading the button and their click arriving inverts what
 *  the click does -- see FLIP_GUARD_MS. This is the clock that guard reads.
 *
 *  It measures the flip, not the render: a card re-rendering for any of the
 *  dozen other reasons the socket sends does not restart the window, so the
 *  guard cannot be worn down by ordinary traffic.
 */
export function useSettledAction(enabled: boolean): {
  action: DestAction;
  /** True while the action has changed too recently to be clicked safely. */
  unsettled: boolean;
} {
  const action = actionFor(enabled);
  const flippedAt = useRef<number>(0);
  const previous = useRef<DestAction | null>(null);
  const [, force] = useState(0);

  if (previous.current !== action) {
    previous.current = action;
    flippedAt.current = Date.now();
  }

  const unsettled = !clickIsTrustworthy(Date.now() - flippedAt.current);

  // Re-render once the guard expires, or the button would stay disabled until
  // some unrelated repaint happened to arrive. On a card whose socket has gone
  // quiet -- which is exactly the state an operator is trying to act on -- that
  // could be for ever.
  //
  // THE DELAY IS WHAT IS LEFT, NOT WHAT HAS PASSED. It was
  // `Date.now() - flippedAt.current` -- the elapsed time, which is ~0 at the
  // moment of a flip. So the self-clearing re-render was scheduled for
  // immediately, arrived while the guard was still live, and recomputed the
  // same `unsettled: true`; with `[unsettled]` for deps nothing had changed,
  // the effect never re-ran, and the timer was never re-armed. The button on a
  // quiet card stayed dead for ever -- the exact failure this effect exists to
  // prevent, in the code written to prevent it.
  //
  // No dependency array on purpose. Every render re-derives the remaining time
  // from the ref and re-arms, so the wake-up is correct no matter which render
  // it was scheduled from, and the `!unsettled` early return terminates it. A
  // dependency list here is what let the one interesting transition -- guarded
  // to guarded, with less time left -- go unnoticed.
  useEffect(() => {
    if (!unsettled) return;
    const left = FLIP_GUARD_MS - (Date.now() - flippedAt.current);
    const t = setTimeout(() => force((n) => n + 1), Math.max(left, 0));
    return () => clearTimeout(t);
  });

  return { action, unsettled };
}

/** A stop that is held briefly and can be taken back.
 *
 *  See STOP_UNDO_MS for why this is undo rather than a confirmation dialog.
 *  The commit runs from a timer, so nothing reaches the server until the window
 *  closes; `cancel` is a real cancellation rather than a compensating action.
 */
export function useHeldStop(commit: () => void, holdMs = STOP_UNDO_MS): {
  /** True while a stop is waiting to be sent. */
  pending: boolean;
  /** Milliseconds left before it is sent. */
  remaining: number;
  hold: () => void;
  cancel: () => void;
} {
  const [deadline, setDeadline] = useState<number | null>(null);
  const [remaining, setRemaining] = useState(0);
  // The latest commit, so a card that re-renders mid-hold does not fire a
  // handler closed over a destination id it no longer describes.
  const latest = useRef(commit);
  latest.current = commit;

  /* Whether a stop is still owed to the server, held in a ref so that every
   * path that ends the hold -- the timer, an undo, and unmounting -- reads and
   * clears the same flag rather than three copies of the question.
   *
   * It replaces the per-effect `fired` local described below and does its job
   * as well: once a tick has committed, `armed` is false for every queued tick
   * that follows, and only `hold()` sets it again. */
  const armed = useRef(false);

  const cancel = useCallback(() => {
    armed.current = false;
    setDeadline(null);
  }, []);
  const hold = useCallback(() => {
    armed.current = true;
    setDeadline(Date.now() + holdMs);
  }, [holdMs]);

  /* A HELD STOP IS OWED, and leaving the page is not an undo.
   *
   * The commit lived only in an interval that React tore down on unmount, so
   * pressing Stop and then navigating away inside the five-second window sent
   * NOTHING -- no request, no toast, no trace. The destination stayed live and
   * the operator had every reason to believe it was stopping, because the card
   * said "Stopping" right up until the moment it stopped existing. A silent
   * discard is the worst of the three possible outcomes: worse than sending,
   * which is what was asked for, and worse than refusing, which at least says
   * so.
   *
   * So it is sent. Undo is a button with a five-second life, and it stays the
   * only way to take a stop back; navigation is not a second, invisible one.
   *
   * THE COST, stated: an operator who pressed Stop and then left the page
   * intending the hold to lapse now gets the stop they asked for. That
   * reading was never true -- it was a bug with an undocumented shape -- but
   * anyone who had learned it will find the behaviour changed. */
  useEffect(
    () => () => {
      if (!armed.current) return;
      armed.current = false;
      latest.current();
    },
    [],
  );

  useEffect(() => {
    if (deadline === null) {
      setRemaining(0);
      return;
    }
    // FIRED ONCE, and this needed a flag rather than trusting the state
    // update. `setDeadline(null)` does not stop the interval synchronously --
    // React schedules the re-render, the already-queued ticks keep arriving
    // against the old closure, and each one saw a deadline in the past. The
    // first version of this hook sent the stop THREE TIMES; on a real card
    // that is three requests to end the same broadcast, and the second and
    // third would arrive after an undo could have been pressed.
    //
    // The flag is now `armed` above, one per hook rather than one per effect
    // run, so the unmount commit cannot double up with a tick that just fired.
    const tick = () => {
      const left = deadline - Date.now();
      if (left <= 0) {
        if (!armed.current) return;
        armed.current = false;
        setDeadline(null);
        latest.current();
        return;
      }
      setRemaining(left);
    };
    tick();
    const id = setInterval(tick, 100);
    return () => clearInterval(id);
  }, [deadline]);

  return { pending: deadline !== null, remaining, hold, cancel };
}
