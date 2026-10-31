import { useSyncExternalStore } from "react";

/** Below Tailwind's `lg` breakpoint (64rem), written as Tailwind v4 writes
 *  `max-lg`. Exported so a test can match its matchMedia stub on it.
 *
 *  THE QUERY ASKS "IS IT NARROW?", NOT "IS IT WIDE?", on purpose. A browser or
 *  test environment with no matchMedia, or a stub that answers `false` to every
 *  query, then gets the desktop layout: today's page, unchanged. jsdom has no
 *  matchMedia at all (see lib/motion.ts), so every existing page test keeps
 *  rendering the layout it was written against. */
export const NARROW_QUERY = "(width < 64rem)";

function media(): MediaQueryList | null {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return null;
  return window.matchMedia(NARROW_QUERY);
}

function subscribe(onChange: () => void): () => void {
  const mq = media();
  if (!mq) return () => {};
  mq.addEventListener("change", onChange);
  return () => mq.removeEventListener("change", onChange);
}

const snapshot = () => media()?.matches ?? false;

/** True below the `lg` breakpoint. Read synchronously on the first render, so
 *  a phone never paints the desktop order first and then jumps. */
export function useIsNarrow(): boolean {
  return useSyncExternalStore(subscribe, snapshot, () => false);
}
