import { useState } from "react";

declare const CONFIRMED: unique symbol;

/** A handler that OPENS A CONFIRMATION rather than doing the thing.
 *
 *  `ask` and a raw deleter have the same shape — `(target: T) => void` — so
 *  they were freely interchangeable at every prop that takes one. A row's
 *  `onDelete` typed that way accepts the immediate deleter exactly as readily
 *  as the confirming one, and the difference between those two is the entire
 *  guard. Nothing at the call site reads differently either: `onDelete={del}`
 *  and `onDelete={confirmDelete.ask}` are the same line of code to a reviewer.
 *
 *  The brand is phantom — it costs nothing at runtime and `ask` is still just
 *  the setter — but it makes the substitution a COMPILE ERROR rather than a
 *  silent loss of the confirmation. Rung 1: a component that requires asking
 *  first can no longer be handed something that does not ask. */
export type Asks<T> = ((target: T) => void) & { readonly [CONFIRMED]: true };

/** State for one pending confirmation, paired with <ConfirmDestructive>.
 *
 *  A hook rather than a pattern to copy, because the copies drift: three pages
 *  each rolling their own `deleting` state is how one of them ends up without a
 *  guard at all, which is exactly what happened with the jobs list.
 *
 *  Six pages use this and the dialog component it feeds. It lives apart from
 *  that component so editing either one can hot-swap: a file exporting both a
 *  component and a hook cannot be, and losing the half-typed filename out of a
 *  confirmation dialog on every save is a poor way to work on the dialog.
 *
 *  The generic is the row being acted on, not a boolean, so `target` carries
 *  what to delete as well as the fact that something is pending. That is what
 *  makes the dialog able to name its subject -- and naming the subject is the
 *  entire safety property. */
export function useConfirm<T>() {
  const [target, setTarget] = useState<T | null>(null);
  return {
    target,
    // The cast is the one place the brand is minted. `setTarget` genuinely is
    // a function that opens the dialog and does nothing else, which is exactly
    // what the brand asserts.
    ask: setTarget as unknown as Asks<T>,
    close: () => setTarget(null),
    open: target !== null,
    onOpenChange: (o: boolean) => {
      if (!o) setTarget(null);
    },
  };
}
