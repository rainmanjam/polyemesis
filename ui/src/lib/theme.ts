import { overlayLocation } from "./overlay";

/** The operator's choice, and what the page is actually wearing. "system" is a
 *  choice, never a theme: it is resolved to one of the two palettes before
 *  anything is painted, so index.css only ever has to define two. */
export type ThemeChoice = "light" | "dark" | "system";
export type Theme = "light" | "dark";

export const THEME_CHOICES: readonly ThemeChoice[] = ["light", "dark", "system"];

/** Namespaced like polyemesis.language and polyemesis.nav.collapsed. Per
 *  browser, on purpose: a theme follows the screen it is read on, and it must
 *  work with no server round trip -- the first paint happens before any API
 *  call can answer. public/theme-init.js reads this same key. */
export const THEME_STORAGE_KEY = "polyemesis.theme";

/** The page colour of each theme, for <meta name="theme-color"> (the browser's
 *  own chrome). They are --surface's value in each palette (what <body> paints),
 *  as hex because a meta tag takes no oklch; palette-contrast.test.ts holds
 *  them to the tokens, and public/theme-init.js and index.html carry the same
 *  two. */
export const THEME_COLOR: Record<Theme, string> = {
  dark: "#0f1115",
  light: "#f1f3f7",
};

/** Whether the operating system is asking for a light page right now. A
 *  browser with no matchMedia (or an old one) is treated as not asking, which
 *  is the console's original theme. */
export function systemPrefersLight(): boolean {
  try {
    return window.matchMedia("(prefers-color-scheme: light)").matches;
  } catch {
    return false;
  }
}

export function resolveTheme(choice: ThemeChoice, prefersLight: boolean): Theme {
  if (choice === "system") return prefersLight ? "light" : "dark";
  return choice;
}

/** The remembered choice, or "system" when there is none, it is not one of the
 *  three, or storage cannot be read. Wrapped because Safari in private mode
 *  throws on ANY localStorage access (see lib/i18n.ts, and useNavCollapsed for
 *  the same guard): a theme is not worth a blank console. */
export function readStoredChoice(): ThemeChoice {
  try {
    const raw = window.localStorage.getItem(THEME_STORAGE_KEY);
    return raw === "light" || raw === "dark" ? raw : "system";
  } catch {
    return "system";
  }
}

/** Remembers a choice. "system" removes the key rather than storing the word,
 *  so a browser that never picked and one that picked system are the same
 *  state. A failed write is not an error: the choice still applies to this
 *  page, it just is not remembered. */
export function storeChoice(choice: ThemeChoice): void {
  try {
    if (choice === "system") window.localStorage.removeItem(THEME_STORAGE_KEY);
    else window.localStorage.setItem(THEME_STORAGE_KEY, choice);
  } catch {
    /* storage unavailable: apply for this page only */
  }
}

/** Whether the theme applies to this page at all. The console is themed; the
 *  public player (/watch) and the OBS overlay pages are NOT, ever -- they are
 *  somebody else's screen, or a layer on somebody's stream, and keep the dark
 *  look they were designed in (index.css's :root, with no data-theme).
 *  Mirrors App.tsx's own watch/overlay decision; public/theme-init.js makes
 *  the same one before the bundle exists, and themeMode.test.ts runs both. */
export function isThemedPath(pathname: string, hash: string): boolean {
  if (pathname === "/watch" || pathname.startsWith("/watch/")) return false;
  return overlayLocation(pathname, hash) === null;
}

/** Puts a theme on the page: the attribute index.css keys on, and the two
 *  <meta> tags the browser reads for its own chrome (form controls and
 *  scrollbars follow color-scheme in CSS; the meta covers the canvas before
 *  the stylesheet and the browser UI after it). */
export function applyTheme(theme: Theme, doc: Document = document): void {
  doc.documentElement.dataset.theme = theme;
  doc.querySelector('meta[name="color-scheme"]')?.setAttribute("content", theme);
  doc.querySelector('meta[name="theme-color"]')?.setAttribute("content", THEME_COLOR[theme]);
}

/** The theme on screen: what data-theme says, dark when nothing has set one
 *  (the :root default, and what unthemed pages wear). */
export function appliedTheme(doc: Document = document): Theme {
  return doc.documentElement.dataset.theme === "light" ? "light" : "dark";
}

/** Every subscriber shares ONE observer: a chat panel subscribes once per
 *  row, and an observer apiece would be hundreds of them watching one attribute.
 *  Created with the first subscriber and released with the last. */
const listeners = new Set<() => void>();
let observer: MutationObserver | null = null;

/** Calls back whenever the page's theme attribute changes, whoever changed it.
 *  Canvas drawing (meters, the timeline) cannot follow a CSS variable on its
 *  own -- it read the colours once -- so it subscribes here and re-reads. */
export function subscribeAppliedTheme(onChange: () => void): () => void {
  if (typeof MutationObserver === "undefined") return () => {};
  listeners.add(onChange);
  if (!observer) {
    observer = new MutationObserver(() => listeners.forEach((l) => l()));
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
  }
  return () => {
    listeners.delete(onChange);
    if (listeners.size === 0) {
      observer?.disconnect();
      observer = null;
    }
  };
}
