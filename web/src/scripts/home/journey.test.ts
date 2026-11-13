// @vitest-environment jsdom

import { readFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { MotionGate } from "./motion";
import { initJourney, stageFor } from "./journey";

describe("stageFor", () => {
  const tops = [100, 500, 900];
  it("is 0 before the first stage", () => {
    expect(stageFor(tops, 0, 0)).toBe(0);
  });
  it("moves to a stage when its top reaches scrollY", () => {
    expect(stageFor(tops, 500, 0)).toBe(1);
    expect(stageFor(tops, 499, 0)).toBe(0);
  });
  it("counts the offset (nav plus bar) as already scrolled", () => {
    expect(stageFor(tops, 480, 64)).toBe(1);
    expect(stageFor(tops, 435, 64)).toBe(0);
  });
  it("stays on the last stage past the end", () => {
    expect(stageFor(tops, 5000, 0)).toBe(2);
  });
  it("is 0 for an empty list", () => {
    expect(stageFor([], 300, 0)).toBe(0);
  });
});

const LABELS = ["Learn", "Understand", "Why polyemesis", "Install", "Use"];
const HREFS = ["#how", "#matrix", "#why", "#install", "#use"];
const STAGES = ["learn", "understand", "why", "install", "use"];

let allowed: boolean;
let gate: MotionGate;
let scrollY: number;
let scrollTo: ReturnType<typeof vi.fn>;
let tops: Record<string, number>;
let teardown: (() => void) | null;

function build(sections = true): HTMLElement {
  document.body.innerHTML = `
    <header><nav></nav></header>
    <nav data-journey aria-label="Page journey">
      <ol>${LABELS.map((l, i) => `<li><a href="${HREFS[i]}" data-journey-link="${STAGES[i]}">${l}</a></li>`).join("")}</ol>
    </nav>
    <main>
      ${
        sections
          ? STAGES.map(
              (s, i) =>
                `<section id="${HREFS[i].slice(1)}" data-journey-stage="${s}"></section>` +
                // a second section of the same stage, and an unrelated data-stage
                (i === 0 ? `<section data-journey-stage="learn" id="again"></section><button data-stage="2"></button>` : ""),
            ).join("")
          : ""
      }
    </main>`;
  document.querySelector("header nav")!.getBoundingClientRect = () => ({ height: 64 }) as DOMRect;
  const root = document.querySelector<HTMLElement>("[data-journey]")!;
  root.getBoundingClientRect = () => ({ height: 44 }) as DOMRect;
  for (const el of document.querySelectorAll<HTMLElement>("[data-journey-stage]")) {
    const id = el.id;
    el.getBoundingClientRect = () => ({ top: (tops[id] ?? 0) - scrollY }) as DOMRect;
  }
  return root;
}

function scrollTick() {
  window.dispatchEvent(new Event("scroll"));
}
function current(): string[] {
  return [...document.querySelectorAll("[data-journey] a[aria-current]")].map((a) => a.textContent ?? "");
}

beforeEach(() => {
  allowed = true;
  gate = {
    allowed: () => allowed,
    userPaused: () => !allowed,
    setUserPaused: () => {},
    subscribe: () => () => {},
  };
  scrollY = 0;
  tops = { how: 700, matrix: 1500, why: 2300, install: 3100, use: 3900, again: 1100 };
  teardown = null;
  scrollTo = vi.fn();
  Object.defineProperty(window, "scrollY", { configurable: true, get: () => scrollY });
  window.scrollTo = scrollTo as unknown as typeof window.scrollTo;
  window.requestAnimationFrame = ((cb: FrameRequestCallback) => {
    cb(0);
    return 0;
  }) as typeof window.requestAnimationFrame;
  window.cancelAnimationFrame = () => {};
});

afterEach(() => {
  teardown?.();
  document.body.innerHTML = "";
});

describe("initJourney", () => {
  it("marks exactly one link current after a scroll", () => {
    teardown = initJourney(build(), gate);
    scrollY = 1500;
    scrollTick();
    expect(current()).toEqual(["Understand"]);
    scrollY = 3900;
    scrollTick();
    expect(current()).toEqual(["Use"]);
  });

  it("allows for the nav and the bar when deciding the current stage", () => {
    teardown = initJourney(build(), gate);
    scrollY = 1500 - 124; // nav 64 + bar 44 + the 16px a clicked section lands below them
    scrollTick();
    expect(current()).toEqual(["Understand"]);
    scrollY = 1500 - 125;
    scrollTick();
    expect(current()).toEqual(["Learn"]);
  });

  it("uses the first section of a stage and ignores data-stage", () => {
    teardown = initJourney(build(), gate);
    scrollY = 1200; // past the second learn section, before understand
    scrollTick();
    expect(current()).toEqual(["Learn"]);
  });

  it("marks the first link before the first section is reached", () => {
    teardown = initJourney(build(), gate);
    scrollY = 0;
    scrollTick();
    expect(current()).toEqual(["Learn"]);
  });

  it("recomputes on resize", () => {
    teardown = initJourney(build(), gate);
    scrollY = 2300;
    window.dispatchEvent(new Event("resize"));
    expect(current()).toEqual(["Why polyemesis"]);
  });

  it("does nothing harmful without any stage element", () => {
    const root = build(false);
    expect(() => {
      teardown = initJourney(root, gate);
      scrollTick();
      window.dispatchEvent(new Event("resize"));
    }).not.toThrow();
    expect(current()).toEqual([]);
  });

  it("stops listening when torn down", () => {
    teardown = initJourney(build(), gate);
    teardown();
    teardown = null;
    scrollY = 3900;
    scrollTick();
    expect(current()).toEqual(["Learn"]); // still what the first pass marked
  });

  it("scrolls smoothly on click when motion is allowed, minus the offset", () => {
    teardown = initJourney(build(), gate);
    const link = document.querySelectorAll<HTMLAnchorElement>("[data-journey] a")[2];
    const ev = new MouseEvent("click", { bubbles: true, cancelable: true });
    link.dispatchEvent(ev);
    expect(ev.defaultPrevented).toBe(true);
    expect(scrollTo).toHaveBeenCalledWith({ top: 2300 - 108 - 16, behavior: "smooth" });
  });

  it("rounds the landing down the page so the clicked stage is the marked one", () => {
    // A section top at 2300.4 minus 124 is 2176.4; a browser that settles on 2176
    // would leave the line at 2300, short of the section, and mark the stage before.
    tops.why = 2300.4;
    teardown = initJourney(build(), gate);
    document.querySelectorAll<HTMLAnchorElement>("[data-journey] a")[2].click();
    expect(scrollTo).toHaveBeenCalledWith({ top: 2177, behavior: "smooth" });
  });

  it("jumps instead of gliding when motion is not allowed", () => {
    allowed = false;
    teardown = initJourney(build(), gate);
    document.querySelectorAll<HTMLAnchorElement>("[data-journey] a")[3].click();
    expect(scrollTo).toHaveBeenCalledWith({ top: 3100 - 108 - 16, behavior: "instant" });
  });

  it.each([
    ["ctrl", { ctrlKey: true }],
    ["meta", { metaKey: true }],
    ["shift", { shiftKey: true }],
    ["middle-button", { button: 1 }],
  ])("leaves a %s click to the browser (open in a new tab must work)", (_name, init) => {
    teardown = initJourney(build(), gate);
    const ev = new MouseEvent("click", { bubbles: true, cancelable: true, ...init });
    document.querySelectorAll("[data-journey] a")[2].dispatchEvent(ev);
    expect(ev.defaultPrevented).toBe(false);
    expect(scrollTo).not.toHaveBeenCalled();
  });

  it("after a plain click moves focus to the section and puts its hash in the URL", () => {
    teardown = initJourney(build(), gate);
    const replace = vi.spyOn(window.history, "replaceState");
    document.querySelectorAll<HTMLAnchorElement>("[data-journey] a")[2].click();
    const why = document.getElementById("why")!;
    expect(why.getAttribute("tabindex")).toBe("-1");
    expect(document.activeElement).toBe(why);
    expect(replace).toHaveBeenCalledWith(null, "", "#why");
    expect(window.location.hash).toBe("#why");
  });

  it("keeps a tabindex the section already has", () => {
    teardown = initJourney(build(), gate);
    document.getElementById("why")!.setAttribute("tabindex", "0");
    document.querySelectorAll<HTMLAnchorElement>("[data-journey] a")[2].click();
    expect(document.getElementById("why")!.getAttribute("tabindex")).toBe("0");
  });

  it("still jumps on an engine that rejects behavior: instant", () => {
    allowed = false;
    scrollTo.mockImplementation((a: unknown) => {
      if (typeof a === "object") throw new TypeError("bad behavior");
    });
    teardown = initJourney(build(), gate);
    expect(() => document.querySelectorAll<HTMLAnchorElement>("[data-journey] a")[3].click()).not.toThrow();
    expect(scrollTo).toHaveBeenLastCalledWith(0, 3100 - 108 - 16);
  });

  it("leaves a click alone when its target is missing", () => {
    teardown = initJourney(build(false), gate);
    const ev = new MouseEvent("click", { bubbles: true, cancelable: true });
    document.querySelectorAll("[data-journey] a")[1].dispatchEvent(ev);
    expect(ev.defaultPrevented).toBe(false);
    expect(scrollTo).not.toHaveBeenCalled();
  });
});

describe("JourneyBar.astro source", () => {
  const src = readFileSync(join(__dirname, "../../components/home/JourneyBar.astro"), "utf8");

  it("is a labelled nav carrying data-journey", () => {
    expect(src).toMatch(/<nav\b[^>]*\bdata-journey\b/);
    expect(src).toMatch(/<nav\b[^>]*\baria-label="[^"]+"/);
  });

  it("has the five links with the right labels and hrefs, in order", () => {
    const pairs = [...src.matchAll(/href:\s*"(#[a-z]+)"[^}]*?label:\s*"([^"]+)"/g)].map((m) => [m[2], m[1]]);
    expect(pairs).toEqual(LABELS.map((l, i) => [l, HREFS[i]]));
  });

  it("names each link's stage with data-journey-link, in order", () => {
    expect([...src.matchAll(/stage:\s*"([a-z]+)"/g)].map((m) => m[1])).toEqual(STAGES);
    expect(src).toMatch(/data-journey-link=/);
  });

  it("does not use .reveal or an outside font", () => {
    expect(src).not.toMatch(/reveal|fonts\.googleapis|@import/);
  });
});

describe("the bar scrolls the active link into view", () => {
  function narrow() {
    const root = build();
    const list = root.querySelector("ol") as HTMLOListElement;
    const spy = vi.fn();
    list.scrollTo = spy as unknown as typeof list.scrollTo;
    list.getBoundingClientRect = () => ({ left: 0, right: 300 }) as DOMRect;
    const links = [...root.querySelectorAll<HTMLAnchorElement>("a")];
    const at = [10, 90, 170, 250, 330]; // five links 70 wide; the last two sit past the 300px edge or on it
    links.forEach((a, i) => (a.getBoundingClientRect = () => ({ left: at[i], right: at[i] + 70 }) as DOMRect));
    return { root, spy };
  }

  it("scrolls the list by the overhang when the stage's link ends past the right edge", () => {
    const { root, spy } = narrow();
    teardown = initJourney(root, gate);
    scrollY = 3900; // the last stage: its link spans 330..400
    scrollTick();
    expect(spy).toHaveBeenCalledWith({ left: 100, behavior: "smooth" });
  });

  it("does not smooth-scroll when motion is off", () => {
    allowed = false;
    const { root, spy } = narrow();
    teardown = initJourney(root, gate);
    scrollY = 3900;
    scrollTick();
    expect(spy).toHaveBeenCalledWith({ left: 100, behavior: "instant" });
  });

  it("leaves a link that is already inside alone", () => {
    const { root, spy } = narrow();
    teardown = initJourney(root, gate);
    scrollY = 1500; // Understand, 90..160
    scrollTick();
    expect(spy).not.toHaveBeenCalled();
  });
});
