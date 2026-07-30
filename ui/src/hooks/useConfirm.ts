import { useState } from "react";

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
    ask: setTarget,
    close: () => setTarget(null),
    open: target !== null,
    onOpenChange: (o: boolean) => {
      if (!o) setTarget(null);
    },
  };
}
