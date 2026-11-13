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
  it("sets depth-3 items apart", () => {
    expect(src).toContain("d3");
  });
});
