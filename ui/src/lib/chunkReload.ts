/* A tab left open across an upgrade.
 *
 * The console's lazy routes are chunks named by content hash, and an upgrade
 * replaces every hash. A tab opened before the upgrade still holds the old
 * index, so the first lazy route it visits asks for a file the server no
 * longer has and the dynamic import rejects. There is nothing to retry: the
 * only cure is loading the new index, which is a reload.
 *
 * Vite announces exactly this failure as a `vite:preloadError` event on window
 * (it covers the chunk itself as well as its preloaded dependencies), and
 * preventDefault() stops it from being thrown. So: reload, once. The guard is
 * what keeps "once" true -- a chunk that is missing for some other reason (a
 * broken deploy, a proxy caching a 404) would otherwise reload the page in a
 * loop. It is a timestamp rather than a flag for the life of the session,
 * because a tab can outlive two upgrades; a second failure inside the window
 * is let through to the route's error boundary, which says what happened and
 * offers the same reload as a button. */

/** How long after an automatic reload another one is refused. */
export const RELOAD_GUARD_MS = 60_000;
const KEY = "polyemesis:chunk-reload-at";

/** Whether an error is a lazy chunk that failed to load, as opposed to a
 *  page that loaded and then crashed. The wording differs by browser:
 *  Chromium, Firefox and Safari each phrase it their own way, and Vite's own
 *  preload failure is the fourth. */
export function isChunkLoadError(err: unknown): boolean {
  if (!(err instanceof Error)) return false;
  return /dynamically imported module|Importing a module script failed|error loading dynamically imported module|Unable to preload CSS|ChunkLoadError/i.test(
    `${err.name} ${err.message}`,
  );
}

interface Deps {
  target: Pick<Window, "addEventListener" | "removeEventListener">;
  storage: Pick<Storage, "getItem" | "setItem"> | null;
  reload: () => void;
  now: () => number;
}

function sessionStore(): Deps["storage"] {
  // Storage can throw on access (a private window, blocked site data). With
  // none, the guard cannot hold, so no automatic reload is attempted at all --
  // the error boundary still offers one.
  try {
    return window.sessionStorage;
  } catch {
    return null;
  }
}

/** Reload once when a chunk fails to load. Returns the uninstall. */
export function installChunkReload(deps: Partial<Deps> = {}): () => void {
  const target = deps.target ?? window;
  const storage = deps.storage === undefined ? sessionStore() : deps.storage;
  const reload = deps.reload ?? (() => window.location.reload());
  const now = deps.now ?? Date.now;

  const onPreloadError = (event: Event) => {
    if (!storage) return;
    try {
      const last = Number(storage.getItem(KEY) ?? 0);
      if (now() - last < RELOAD_GUARD_MS) return;
      storage.setItem(KEY, String(now()));
    } catch {
      return;
    }
    event.preventDefault();
    reload();
  };
  target.addEventListener("vite:preloadError", onPreloadError);
  return () => target.removeEventListener("vite:preloadError", onPreloadError);
}
