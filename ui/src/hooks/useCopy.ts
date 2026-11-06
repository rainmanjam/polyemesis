import { useCallback, useEffect, useRef, useState } from "react";
import { writeClipboard } from "@/lib/clipboard";

/** Copy to the clipboard with the "copied" flag that resets itself.
 *
 *  `copy` resolves true when the text reached the clipboard and false when it
 *  did not (no clipboard on plain http, or a refused write); it never throws,
 *  so a click handler cannot leave an unhandled rejection. A caller that wants
 *  the operator told passes `onFail`; one that shows the text beside the button
 *  anyway can leave it out. `key` names WHAT was copied for a list of buttons
 *  that share one hook: `copiedKey` is the key of the last successful copy
 *  until `resetMs` passes. */
export function useCopy(resetMs = 1500, onFail?: () => void) {
  const [copiedKey, setCopiedKey] = useState<string | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const failRef = useRef(onFail);
  failRef.current = onFail;

  useEffect(() => () => clearTimeout(timer.current), []);

  const copy = useCallback(
    async (text: string, key = ""): Promise<boolean> => {
      const ok = await writeClipboard(text);
      if (!ok) {
        failRef.current?.();
        return false;
      }
      setCopiedKey(key);
      clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopiedKey(null), resetMs);
      return true;
    },
    [resetMs],
  );

  return { copy, copied: copiedKey !== null, copiedKey };
}
