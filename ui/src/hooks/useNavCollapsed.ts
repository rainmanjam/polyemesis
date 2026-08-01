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

/** Whether a keystroke belongs to something the user is typing into.
 *
 *  This is the app's FIRST global key listener -- every other handler is
 *  element-scoped, on the chat composer, the anchor grid, the confirm dialog and
 *  the upload dropzone. So this predicate is the pattern the next shortcut will
 *  copy, and it is written to be copied.
 *
 *  polyemesis has a chat panel. Someone typing Cmd+B mid-message must not lose
 *  their sidebar, and a product where that happens once is a product where the
 *  shortcut gets turned off.
 */
function isTyping(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  return ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName);
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

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      // metaKey for macOS, ctrlKey elsewhere. Firefox binds this combination
      // at browser-chrome level to "Toggle Bookmarks Sidebar", and
      // preventDefault below deliberately takes it over whenever focus is
      // outside a text field -- not "the browser binds nothing here". That
      // trade-off is accepted because Ctrl/Cmd+B is the convention VS Code,
      // Slack and Notion already trained users on for a collapsible sidebar.
      if (e.key.toLowerCase() !== "b" || !(e.metaKey || e.ctrlKey)) return;
      if (isTyping(e.target)) return;
      e.preventDefault();
      setCollapsed((v) => !v);
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const toggle = useCallback(() => setCollapsed((v) => !v), []);
  return [collapsed, toggle];
}
