/** Client-only React keys for the rows of an editable list whose items carry no
 *  id of their own.
 *
 *  Index keys are wrong for such a list: removing row 2 of 3 makes the old row
 *  3 take over row 2's key, so React reuses row 2's inputs for the wrong data
 *  and their focus and typed text stay behind. A key that follows the row is
 *  what keeps the other rows' inputs intact.
 *
 *  The key lives in a WeakMap beside the row, never on it, so nothing the save
 *  path sends to the API can pick it up. A row that is edited is replaced by a
 *  new object; `replace` hands the key across to that object so the row keeps
 *  its identity in the DOM while it is typed into. */
export type RowKeys<T extends object> = {
  /** The row's key, minted on first sight. Safe to call during render. */
  keyOf(row: T): string;
  /** `next` takes over `prev`'s key. Returns `next`, so it can sit inside a map. */
  replace(prev: T, next: T): T;
};

let minted = 0;

export function createRowKeys<T extends object>(prefix: string): RowKeys<T> {
  const keys = new WeakMap<T, string>();
  const keyOf = (row: T): string => {
    let key = keys.get(row);
    if (key === undefined) {
      minted += 1;
      key = `${prefix}-${minted}`;
      keys.set(row, key);
    }
    return key;
  };
  return {
    keyOf,
    replace(prev, next) {
      keys.set(next, keyOf(prev));
      return next;
    },
  };
}
