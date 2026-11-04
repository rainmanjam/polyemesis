import { useCallback, useRef } from "react";

/** Props for a Radix <DialogContent> that holds a form someone is typing into.
 *
 *  A STRAY GESTURE MUST NOT DISCARD A FORM (poka-yoke audit 2026-11-04). The
 *  destination dialog is four steps long and carries a stream key; a click on
 *  the scrim or an Esc closed it with every field gone. So:
 *
 *  - a click or focus OUTSIDE the dialog never dismisses it -- there is no form
 *    for which "I clicked beside it" means "throw this away";
 *  - Esc dismisses only while nothing has been typed. Once any field inside has
 *    changed, Esc is refused and `onBlocked` says why. Cancel and the close
 *    button still close: those are a decision, not a slip.
 *
 *  "Touched" is any input or change event inside the content (every text
 *  field), and any click or Enter/Space press: Radix Select, Switch and
 *  Checkbox, and the destination dialog's preset buttons, change state without
 *  a native input event. React's synthetic events bubble through portals, so a
 *  Select's portaled list counts too. Over-marking is the safe direction: the
 *  worst case is that Esc asks the operator to use Cancel. Call `reset()` when
 *  the dialog opens on a new subject. */
export function useDismissGuard(onBlocked: () => void) {
  const touched = useRef(false);
  const reset = useCallback(() => {
    touched.current = false;
  }, []);
  const mark = useCallback(() => {
    touched.current = true;
  }, []);
  const contentProps = {
    onInputCapture: mark,
    onChangeCapture: mark,
    onClickCapture: mark,
    onKeyDownCapture: (e: { key: string }) => {
      if (e.key === "Enter" || e.key === " ") mark();
    },
    onInteractOutside: (e: Event) => e.preventDefault(),
    onEscapeKeyDown: (e: KeyboardEvent) => {
      if (touched.current) {
        e.preventDefault();
        onBlocked();
      }
    },
  };
  return { contentProps, reset, touched };
}
