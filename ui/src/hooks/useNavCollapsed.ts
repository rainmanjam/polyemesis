import { useCallback, useEffect, useState } from "react";

/** Where the preference lives. Namespaced like `polyemesis.language`, which is
 *  the only other key this app stores. */
const STORAGE_KEY = "polyemesis.nav.collapsed";

/** Reads the stored preference, defaulting to expanded.
 *
 *  Wrapped because Safari in private mode throws on ANY localStorage access --
 *  see the same guard and the same reasoning in lib/i18n.ts. A sidebar
 *  preference is not worth breaking the app shell over, and expanded is a
 *  working default.
 */
function readStored(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) === "true";
  } catch {
    return false;
  }
}

/** The collapsed state of the desktop sidebar, and a toggle for it.
 *
 *  Read once on mount rather than subscribed to: two tabs disagreeing about a
 *  sidebar width is not a bug worth a `storage` listener.
 */
export function useNavCollapsed(): [boolean, () => void] {
  const [collapsed, setCollapsed] = useState(readStored);

  useEffect(() => {
    try {
      localStorage.setItem(STORAGE_KEY, String(collapsed));
    } catch {
      // Nothing to do and nothing to report: the preference simply does not
      // survive this session, which is the documented Safari behaviour.
    }
  }, [collapsed]);

  const toggle = useCallback(() => setCollapsed((v) => !v), []);
  return [collapsed, toggle];
}
