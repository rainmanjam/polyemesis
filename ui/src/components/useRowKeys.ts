import { useState } from "react";

/* React keys for an editable list whose rows have no id of their own.
 *
 * Rows are added, removed and edited in place, so an index key would hand a
 * row's DOM node (and its focus and typed text) to whichever row slid into its
 * slot. These keys are client-only: they sit in their own array beside the
 * rows, never inside them, so nothing sent to the API gains a field. */

let lastKey = 0;
const freshKey = (): number => {
  lastKey += 1;
  return lastKey;
};

export interface RowKeys {
  /** The React key for row `i`. */
  at: (i: number) => number;
  /** Call in the same handler that appends a row. */
  add: () => void;
  /** Call in the same handler that removes row `i`. */
  remove: (i: number) => void;
}

/** Keys for a list of `count` rows. The list itself stays in the caller's
 *  state; this only tracks identity. When the list is replaced from outside
 *  (a load, a reset), the keys are realigned to the new length, keeping the
 *  ones that still line up with a row. */
export function useRowKeys(count: number): RowKeys {
  const [keys, setKeys] = useState<number[]>(() => Array.from({ length: count }, freshKey));
  let current = keys;
  if (keys.length !== count) {
    current = Array.from({ length: count }, (_, i) => keys[i] ?? freshKey());
    setKeys(current);
  }
  return {
    at: (i) => current[i],
    add: () => setKeys((ks) => [...ks, freshKey()]),
    remove: (i) => setKeys((ks) => ks.filter((_, j) => j !== i)),
  };
}
