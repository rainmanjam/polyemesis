// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { initPageIndex as init } from "./page-index";

/* Every index a test starts is disposed after it, so no scroll listener outlives its test. */
const disposers: Array<() => void> = [];
const initPageIndex = (root: HTMLElement) => {
  const dispose = init(root);
  disposers.push(dispose);
  return dispose;
};

const TOPS: Record<string, number> = { a: 100, b: 500, c: 900 };

function page(withSections = true): HTMLElement {
  const link = (id: string) => `<li><a href="#${id}">${id}</a></li>`;
  const copy = (cls: string) => `<ol class="${cls}">${["a", "b", "c"].map(link).join("")}</ol>`;
  document.body.innerHTML =
    `<div data-index>${copy("rail")}${copy("phone")}</div>` +
    (withSections ? ["a", "b", "c"].map((id) => `<section id="${id}"></section>`).join("") : "");
  for (const id of Object.keys(TOPS)) {
    const el = document.getElementById(id);
    if (el) el.getBoundingClientRect = () => ({ top: TOPS[id] - window.scrollY }) as DOMRect;
  }
  return document.querySelector<HTMLElement>("[data-index]")!;
}

const setScroll = (y: number) => {
  Object.defineProperty(window, "scrollY", { value: y, configurable: true });
  window.dispatchEvent(new Event("scroll"));
};
const current = (root: HTMLElement, copy: string) =>
  [...root.querySelectorAll<HTMLAnchorElement>(`.${copy} a`)].map((a) => a.getAttribute("aria-current") === "true");

afterEach(() => {
  for (const d of disposers.splice(0)) d();
  vi.unstubAllGlobals();
  Object.defineProperty(window, "scrollY", { value: 0, configurable: true });
  document.body.innerHTML = "";
});

describe("initPageIndex", () => {
  const runNow = () => vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => (cb(0), 1));

  it("marks the section under the sticky nav, in both copies of the list", () => {
    runNow();
    Object.defineProperty(window, "scrollY", { value: 480, configurable: true });
    const root = page();
    initPageIndex(root);
    expect(current(root, "rail")).toEqual([false, true, false]);
    expect(current(root, "phone")).toEqual([false, true, false]);
  });

  it("marks the first at the top and the last at the bottom, and follows scrolling", () => {
    runNow();
    const root = page();
    initPageIndex(root);
    expect(current(root, "rail")).toEqual([true, false, false]);
    setScroll(5000);
    expect(current(root, "rail")).toEqual([false, false, true]);
    expect(current(root, "phone")).toEqual([false, false, true]);
    setScroll(0);
    expect(current(root, "rail")).toEqual([true, false, false]);
  });

  describe("the line is the sections' own scroll margin plus 8px", () => {
    const withMargin = (px: number) => {
      document.head.innerHTML = `<style>section { scroll-margin-top: ${px}px; }</style>`;
    };
    afterEach(() => {
      document.head.innerHTML = "";
      Object.defineProperty(document.documentElement, "scrollHeight", { value: 0, configurable: true });
    });

    it("counts a section sitting exactly at its scroll margin as the current one", () => {
      runNow();
      withMargin(128); // a phone: nav plus strip
      const root = page();
      Object.defineProperty(window, "scrollY", { value: 372, configurable: true }); // b's top (500) sits at 128
      initPageIndex(root);
      expect(current(root, "rail")).toEqual([false, true, false]);
      setScroll(364); // b is 8px below its margin: still counts as arrived
      expect(current(root, "rail")).toEqual([false, true, false]);
      setScroll(363); // 9px below: not yet
      expect(current(root, "rail")).toEqual([true, false, false]);
    });

    it("still keeps the previous section while the next is further below the margin", () => {
      runNow();
      withMargin(80);
      const root = page();
      Object.defineProperty(window, "scrollY", { value: 300, configurable: true }); // b at 200 from the top, line 88
      initPageIndex(root);
      expect(current(root, "rail")).toEqual([true, false, false]);
    });

    it("reads the margin again on resize", () => {
      runNow();
      withMargin(80);
      const root = page();
      Object.defineProperty(window, "scrollY", { value: 372, configurable: true });
      initPageIndex(root);
      expect(current(root, "rail")).toEqual([true, false, false]); // line 88: b (500) is not reached
      withMargin(128);
      window.dispatchEvent(new Event("resize"));
      expect(current(root, "rail")).toEqual([false, true, false]);
    });

    it("falls back to 120 when the page gives no margin (0px, or not a number)", () => {
      runNow();
      const root = page(); // no stylesheet
      Object.defineProperty(window, "scrollY", { value: 380, configurable: true }); // b at 120 from the top
      initPageIndex(root);
      expect(current(root, "rail")).toEqual([false, true, false]);
      setScroll(379);
      expect(current(root, "rail")).toEqual([true, false, false]);
      vi.spyOn(window, "getComputedStyle").mockReturnValue({ scrollMarginTop: "auto" } as CSSStyleDeclaration);
      setScroll(380);
      expect(current(root, "rail")).toEqual([false, true, false]);
    });
  });

  it("writes aria-current only when the current section changes", () => {
    runNow();
    const root = page();
    initPageIndex(root);
    const writes = vi.spyOn(Element.prototype, "setAttribute");
    const removes = vi.spyOn(Element.prototype, "removeAttribute");
    setScroll(10);
    setScroll(20);
    expect(writes).not.toHaveBeenCalled();
    expect(removes).not.toHaveBeenCalled();
    setScroll(480);
    expect(writes).toHaveBeenCalled();
    expect(current(root, "rail")).toEqual([false, true, false]);
  });

  it("marks the last link when the page is scrolled to the bottom, though the last section never reaches the line", () => {
    runNow();
    const root = page();
    Object.defineProperty(document.documentElement, "scrollHeight", { value: 1470, configurable: true });
    initPageIndex(root);
    setScroll(700); // c is at 900: below the line (820), and the viewport (768) ends at 1468
    expect(current(root, "rail")).toEqual([false, false, true]);
    setScroll(600);
    expect(current(root, "rail")).toEqual([false, true, false]);
    Object.defineProperty(document.documentElement, "scrollHeight", { value: 0, configurable: true });
  });

  it("does nothing after the disposer runs", () => {
    runNow();
    const root = page();
    const dispose = initPageIndex(root);
    dispose();
    setScroll(5000);
    expect(current(root, "rail")).toEqual([true, false, false]);
  });

  it("does one update per frame however many scrolls arrive", () => {
    const queued: FrameRequestCallback[] = [];
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => queued.push(cb));
    const root = page();
    initPageIndex(root);
    queued.length = 0;
    setScroll(100);
    setScroll(200);
    setScroll(300);
    expect(queued).toHaveLength(1);
    queued[0](0);
    setScroll(400);
    expect(queued).toHaveLength(2);
  });

  it("cancels a pending frame when disposed", () => {
    const cancel = vi.fn();
    vi.stubGlobal("requestAnimationFrame", () => 7);
    vi.stubGlobal("cancelAnimationFrame", cancel);
    const root = page();
    const dispose = initPageIndex(root);
    setScroll(10);
    dispose();
    expect(cancel).toHaveBeenCalledWith(7);
  });

  it("ignores links whose section is not on the page, and a root with no links", () => {
    runNow();
    const root = page(false);
    expect(() => initPageIndex(root)()).not.toThrow();
    expect(current(root, "rail")).toEqual([false, false, false]);
    const empty = document.createElement("div");
    expect(() => initPageIndex(empty)()).not.toThrow();
  });

  it("ignores a link whose fragment cannot be decoded", () => {
    runNow();
    const root = page();
    root.querySelector(".rail")!.insertAdjacentHTML("beforeend", '<li><a href="#%">bad</a></li>');
    expect(() => initPageIndex(root)).not.toThrow();
    expect(current(root, "rail")).toEqual([true, false, false, false]);
  });

  it("returns a no-op when the document has no window", () => {
    const doc = document.implementation.createHTMLDocument("x");
    const root = doc.createElement("div");
    expect(() => initPageIndex(root)()).not.toThrow();
  });
});

describe("PageIndex.astro", () => {
  const src = readFileSync(join(__dirname, "../components/inner/PageIndex.astro"), "utf8");
  it("renders the rail and the phone form, each only for its variant", () => {
    expect(src).toMatch(/variant !== "phone"[\s\S]*class="rail"/);
    expect(src).toMatch(/variant !== "rail"[\s\S]*idx-strip/);
  });
  it("uses a details element for the disclosure form", () => {
    expect(src).toContain("<details");
    expect(src).toMatch(/phone === "disclosure"/);
  });
  /* vitest has no Astro plugin, so the component is not rendered here: its source is read. Each of
   * the three forms must list the same items, in the same order, with the same link and the same
   * depth class, which is what the one `items.map(...)` body repeated three times promises. */
  const bodies = [...src.matchAll(/items\.map\(\(it\) => \(([\s\S]*?)\)\)\}/g)].map((m) => m[1].replace(/\s+/g, " ").trim());
  it("lists the items the same way in all three forms", () => {
    expect(bodies).toHaveLength(3);
    expect(new Set(bodies).size).toBe(1);
    expect(bodies[0]).toBe('<li class={it.depth === 3 ? "d3" : undefined}> <a href={`#${it.id}`}>{it.label}</a> </li>');
  });
  it("sets only depth-3 items apart", () => {
    expect(src.match(/"d3"/g)).toHaveLength(3); // the three loops and nothing else
    expect(src).not.toMatch(/it\.depth === 2/);
  });
});
