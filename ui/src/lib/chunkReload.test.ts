/// <reference types="node" />
import { describe, expect, it, vi } from "vitest";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

import { RELOAD_GUARD_MS, installChunkReload, isChunkLoadError } from "./chunkReload";

/* A tab left open across an upgrade asks for chunks the server no longer has.
 * Vite reports that as `vite:preloadError`; the console reloads once to pick
 * up the new index, and refuses a second reload inside the guard window so a
 * chunk missing for some other reason cannot loop the page. */

function harness(now: { t: number }) {
  const target = new EventTarget();
  const store = new Map<string, string>();
  const reload = vi.fn();
  installChunkReload({
    target: target as unknown as Window,
    storage: { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => void store.set(k, v) },
    reload,
    now: () => now.t,
  });
  const fire = () => {
    const ev = new Event("vite:preloadError", { cancelable: true });
    target.dispatchEvent(ev);
    return ev;
  };
  return { reload, fire };
}

describe("installChunkReload", () => {
  it("reloads on the first failed chunk and swallows the error", () => {
    const { reload, fire } = harness({ t: 1_000_000 });
    const ev = fire();
    expect(reload).toHaveBeenCalledTimes(1);
    expect(ev.defaultPrevented).toBe(true);
  });

  it("does not loop: a second failure inside the window reaches the boundary", () => {
    const now = { t: 1_000_000 };
    const { reload, fire } = harness(now);
    fire();
    now.t += RELOAD_GUARD_MS - 1;
    const ev = fire();
    expect(reload).toHaveBeenCalledTimes(1);
    expect(ev.defaultPrevented).toBe(false);
  });

  it("reloads again for a later upgrade in the same tab", () => {
    const now = { t: 1_000_000 };
    const { reload, fire } = harness(now);
    fire();
    now.t += RELOAD_GUARD_MS + 1;
    fire();
    expect(reload).toHaveBeenCalledTimes(2);
  });

  it("attempts nothing when storage is unavailable, because the guard cannot hold", () => {
    const target = new EventTarget();
    const reload = vi.fn();
    installChunkReload({ target: target as unknown as Window, storage: null, reload });
    target.dispatchEvent(new Event("vite:preloadError", { cancelable: true }));
    expect(reload).not.toHaveBeenCalled();
  });
});

describe("isChunkLoadError", () => {
  it("recognises each browser's wording", () => {
    expect(isChunkLoadError(new TypeError("Failed to fetch dynamically imported module: /a.js"))).toBe(true);
    expect(isChunkLoadError(new TypeError("Importing a module script failed."))).toBe(true);
    expect(isChunkLoadError(new TypeError("error loading dynamically imported module: /a.js"))).toBe(true);
    expect(isChunkLoadError(new Error("Unable to preload CSS for /a.css"))).toBe(true);
  });
  it("does not call an ordinary crash a stale tab", () => {
    expect(isChunkLoadError(new TypeError("Cannot read properties of undefined"))).toBe(false);
    expect(isChunkLoadError("nope")).toBe(false);
  });
});

/* THE STRUCTURAL HALF. A <Suspense> around a lazy chunk with no error boundary
 * is the blank page: the chunk's rejection unmounts everything. LazyBoundary
 * is Suspense and a boundary together, so the rule is simply that nothing in
 * src/ writes <Suspense> itself. */
const SRC = fileURLToPath(new URL("..", import.meta.url));
function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return sources(path);
    if (!/\.tsx$/.test(name) || /\.test\.tsx$/.test(name)) return [];
    return [path];
  });
}

describe("lazy chunks", () => {
  it("are only ever suspended inside LazyBoundary", () => {
    const offenders = sources(SRC)
      .filter((f) => !f.endsWith(join("components", "ErrorBoundary.tsx")))
      .filter((f) => /<Suspense[\s>]/.test(readFileSync(f, "utf8")));
    expect(offenders.map((f) => relative(SRC, f))).toEqual([]);
  });
});
