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

  const cancel = useCallback(() => setDeadline(null), []);
  const hold = useCallback(() => setDeadline(Date.now() + holdMs), [holdMs]);

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
    let fired = false;
    const tick = () => {
      const left = deadline - Date.now();
      if (left <= 0) {
        if (fired) return;
        fired = true;
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
