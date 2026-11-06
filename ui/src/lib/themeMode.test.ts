// @vitest-environment jsdom
/// <reference types="node" />
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  THEME_STORAGE_KEY,
  appliedTheme,
  applyTheme,
  isThemedPath,
  readStoredChoice,
  resolveTheme,
  storeChoice,
  subscribeAppliedTheme,
} from "./theme";

/* THE THEME IS A PER-BROWSER PREFERENCE WITH THREE STATES, and these are the
 * rules the rest of the console leans on:
 *   - "system" is the default and follows the OS until the operator picks;
 *   - an explicit light/dark wins over the OS;
 *   - storage may be missing or throw (Safari private mode), and the console
 *     must still open, in the system's theme;
 *   - the public player and the OBS overlay are never themed. */

const html = () => document.documentElement;

beforeEach(() => {
  localStorage.clear();
  html().removeAttribute("data-theme");
  document.head.innerHTML = "";
});
afterEach(() => vi.restoreAllMocks());

describe("resolveTheme", () => {
  it.each([
    ["light", true, "light"],
    ["light", false, "light"],
    ["dark", true, "dark"],
    ["dark", false, "dark"],
    ["system", true, "light"],
    ["system", false, "dark"],
  ] as const)("%s with the system preferring light=%s is %s", (choice, prefersLight, want) => {
    expect(resolveTheme(choice, prefersLight)).toBe(want);
  });
});

describe("the stored choice", () => {
  it("is system until the operator picks", () => {
    expect(readStoredChoice()).toBe("system");
  });

  it.each(["light", "dark"] as const)("remembers %s", (choice) => {
    storeChoice(choice);
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe(choice);
    expect(readStoredChoice()).toBe(choice);
  });

  it("forgets the key when the operator goes back to system, so the OS is followed again", () => {
    storeChoice("dark");
    storeChoice("system");
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBeNull();
    expect(readStoredChoice()).toBe("system");
  });

  it("treats a value nobody wrote as system rather than as a theme", () => {
    localStorage.setItem(THEME_STORAGE_KEY, "solarized");
    expect(readStoredChoice()).toBe("system");
  });

  it("survives storage that throws on every access", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new DOMException("denied", "SecurityError");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("denied", "SecurityError");
    });
    vi.spyOn(Storage.prototype, "removeItem").mockImplementation(() => {
      throw new DOMException("denied", "SecurityError");
    });
    expect(readStoredChoice()).toBe("system");
    expect(() => storeChoice("dark")).not.toThrow();
    expect(() => storeChoice("system")).not.toThrow();
  });
});

describe("which pages are themed", () => {
  it.each(["/", "/sources", "/routing/3", "/settings", "/overlays-help", "/watching", "/overlay/nonsense"])("themes the console page %s", (p) => {
    expect(isThemedPath(p, "")).toBe(true);
  });

  it.each([
    "/watch",
    "/watch/",
    "/watch/some-stream",
    "/overlay",
    "/overlay/",
    "/overlay/dock",
    "/overlay/poll",
    "/overlay/k/abc123",
    "/overlay/dock/k/abc123",
  ])("never themes the public or overlay page %s", (p) => {
    expect(isThemedPath(p, "")).toBe(false);
  });
});

describe("applyTheme", () => {
  it("sets data-theme and tells the browser which scheme the page is", () => {
    document.head.innerHTML = '<meta name="color-scheme" content="dark"><meta name="theme-color" content="#0f1115">';
    applyTheme("light");
    expect(html().dataset.theme).toBe("light");
    expect(document.querySelector('meta[name="color-scheme"]')!.getAttribute("content")).toBe("light");
    // The browser chrome follows the shell colour of the theme.
    expect(document.querySelector('meta[name="theme-color"]')!.getAttribute("content")).toBe("#f1f3f7");
    applyTheme("dark");
    expect(html().dataset.theme).toBe("dark");
    expect(document.querySelector('meta[name="color-scheme"]')!.getAttribute("content")).toBe("dark");
    expect(document.querySelector('meta[name="theme-color"]')!.getAttribute("content")).toBe("#0f1115");
  });

  it("works with no meta tags in the document", () => {
    expect(() => applyTheme("light")).not.toThrow();
  });

  it("reports the theme on screen, dark when nothing has set one", () => {
    expect(appliedTheme()).toBe("dark");
    applyTheme("light");
    expect(appliedTheme()).toBe("light");
  });
});

/* public/theme-init.js is the same decision made before first paint, in plain
 * ES5, because the CSP forbids an inline script and a module cannot run before
 * the stylesheet. Two implementations of one rule drift, so this runs the real
 * file against the real module over the same cases. */
describe("public/theme-init.js agrees with lib/theme.ts", () => {
  const SRC = readFileSync(join(process.cwd(), "public", "theme-init.js"), "utf8");

  function run(opts: { path: string; stored?: string | null; prefersLight: boolean; storageThrows?: boolean }) {
    html().removeAttribute("data-theme");
    document.head.innerHTML = '<meta name="color-scheme" content="dark"><meta name="theme-color" content="#0f1115">';
    localStorage.clear();
    if (opts.stored != null) localStorage.setItem(THEME_STORAGE_KEY, opts.stored);
    if (opts.storageThrows) {
      vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
        throw new DOMException("denied", "SecurityError");
      });
    }
    history.replaceState(null, "", opts.path);
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("light") && opts.prefersLight }));
    new Function(SRC)();
    const out = html().dataset.theme ?? null;
    vi.unstubAllGlobals();
    return out;
  }

  const cases = [
    { path: "/", prefersLight: true },
    { path: "/", prefersLight: false },
    { path: "/sources", stored: "dark", prefersLight: true },
    { path: "/sources", stored: "light", prefersLight: false },
    { path: "/", stored: "system", prefersLight: true },
    { path: "/", stored: "garbage", prefersLight: true },
    { path: "/", prefersLight: true, storageThrows: true },
    { path: "/", prefersLight: false, storageThrows: true },
    { path: "/watch", stored: "light", prefersLight: true },
    { path: "/watch/x", stored: "light", prefersLight: true },
    { path: "/overlay", stored: "light", prefersLight: true },
    { path: "/overlay/dock/k/abc", stored: "light", prefersLight: true },
    { path: "/overlay/poll/", stored: "light", prefersLight: true },
    { path: "/overlay/nonsense", stored: "light", prefersLight: false },
    { path: "/watching", stored: "light", prefersLight: false },
  ];

  it.each(cases)("picks the same theme as the module: %j", (c) => {
    // run() seeds localStorage (and, for storageThrows, leaves the throwing
    // spy in place), so the REAL readStoredChoice sees what the script saw.
    const fromScript = run(c);
    const want = isThemedPath(c.path, "") ? resolveTheme(readStoredChoice(), c.prefersLight) : null;
    expect(fromScript).toBe(want);
  });

  it("does not touch the meta tags on a page it does not theme", () => {
    run({ path: "/watch", stored: "light", prefersLight: true });
    expect(document.querySelector('meta[name="color-scheme"]')!.getAttribute("content")).toBe("dark");
    expect(document.querySelector('meta[name="theme-color"]')!.getAttribute("content")).toBe("#0f1115");
  });

  it("flips the meta tags with the theme on a console page", () => {
    run({ path: "/", stored: "light", prefersLight: false });
    expect(document.querySelector('meta[name="color-scheme"]')!.getAttribute("content")).toBe("light");
    expect(document.querySelector('meta[name="theme-color"]')!.getAttribute("content")).toBe("#f1f3f7");
  });
});

describe("index.html loads the theme before the first paint, within the CSP", () => {
  const INDEX = readFileSync(join(process.cwd(), "index.html"), "utf8");
  const scripts = [...INDEX.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/g)];

  it("loads /theme-init.js as a plain synchronous script in <head>, before the module bundle", () => {
    const head = INDEX.slice(0, INDEX.indexOf("</head>"));
    const tag = /<script\b[^>]*src="\/theme-init\.js"[^>]*>/.exec(head);
    expect(tag, "theme-init.js must be loaded from <head>").not.toBeNull();
    // async, defer or type=module would run it after the first paint.
    expect(tag![0]).not.toMatch(/\b(async|defer|type=)/);
    expect(INDEX.indexOf("/theme-init.js")).toBeLessThan(INDEX.indexOf("/src/main.tsx"));
  });

  it("comes after the two meta tags it updates", () => {
    const at = INDEX.indexOf("/theme-init.js");
    expect(INDEX.indexOf('name="color-scheme"')).toBeLessThan(at);
    expect(INDEX.indexOf('name="theme-color"')).toBeLessThan(at);
  });

  it("has no inline script, because the CSP forbids one", () => {
    // Every <script> in the shell names a file; none has a body.
    for (const [, attrs, body] of scripts) {
      expect(attrs, "an inline <script> would be blocked by the server's CSP").toMatch(/\bsrc="/);
      expect(body.trim()).toBe("");
    }
  });

  it("does not paint the body inline, so an overlay's own transparent body is the only one", () => {
    expect(/<body[^>]*\bstyle=/.test(INDEX)).toBe(false);
  });
});

describe("subscribeAppliedTheme", () => {
  it("shares one observer however many rows subscribe, and lets go when the last leaves", async () => {
    const observe = vi.spyOn(MutationObserver.prototype, "observe");
    const disconnect = vi.spyOn(MutationObserver.prototype, "disconnect");
    const calls = [vi.fn(), vi.fn(), vi.fn()];
    const off = calls.map((c) => subscribeAppliedTheme(c));
    expect(observe).toHaveBeenCalledTimes(1);

    applyTheme("light");
    await Promise.resolve();
    for (const c of calls) expect(c).toHaveBeenCalledTimes(1);

    off[0]();
    off[1]();
    expect(disconnect).not.toHaveBeenCalled();
    off[2]();
    expect(disconnect).toHaveBeenCalledTimes(1);
  });
});
