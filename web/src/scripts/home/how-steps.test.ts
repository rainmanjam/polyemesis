// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { MotionGate } from "./motion";
import { initHowSteps, progressOf, stepFor } from "./how-steps";

const VH = 900;
const TRACK_H = 4000;
const RANGE = TRACK_H - VH;

describe("progressOf", () => {
  it("clamps to 0..1", () => {
    expect(progressOf(100, TRACK_H, VH)).toBe(0);
    expect(progressOf(-1550, TRACK_H, VH)).toBe(0.5);
    expect(progressOf(-9999, TRACK_H, VH)).toBe(1);
  });
  it("starts when the track reaches the nav, not the viewport top", () => {
    expect(progressOf(56, TRACK_H, VH, 56)).toBe(0);
    expect(progressOf(30, TRACK_H, VH, 56)).toBeGreaterThan(0);
    // ends when the track's bottom meets the viewport's bottom
    expect(progressOf(VH - TRACK_H, TRACK_H, VH, 56)).toBe(1);
  });
  it("does not divide by zero when the track is no taller than the viewport", () => {
    expect(progressOf(-10, 900, 900)).toBe(0);
  });
});

describe("stepFor", () => {
  it("maps thirds", () => {
    expect(stepFor(0, 3)).toBe(1);
    expect(stepFor(0.32, 3)).toBe(1);
    expect(stepFor(1 / 3, 3)).toBe(2);
    expect(stepFor(0.5, 3)).toBe(2);
    expect(stepFor(2 / 3, 3)).toBe(3);
    expect(stepFor(0.99, 3)).toBe(3);
    expect(stepFor(1, 3)).toBe(3);
  });
  it("defaults to the three steps", () => {
    expect(stepFor(0.5)).toBe(2);
    expect(stepFor(1)).toBe(3);
  });
});

let allowed: boolean;
let gateSubs: Array<(a: boolean) => void>;
let gate: MotionGate;
let trackTop: number;
let ioCallback: ((entries: Array<{ isIntersecting: boolean; intersectionRatio: number }>) => void) | null;
let wide: boolean;
let mqListeners: Array<() => void>;
let navHeight: number;
let journeyHeight: number | null; // null: no journey bar on the page
let scrollTo: ReturnType<typeof vi.fn>;
let teardown: (() => void) | null;

const STEPS = [1, 2, 3];

function build(): HTMLElement {
  document.body.innerHTML = `
    <header><nav></nav></header>
    ${journeyHeight === null ? "" : '<nav data-journey></nav>'}
    <section id="how">
      <div class="how-track">
        <div class="how-stage">
          <ol class="steps">${STEPS.map((n) => `<li class="step" data-step="${n}"><h3>${n}</h3><a class="step-link" href="#s${n}">link ${n}</a></li>`).join("")}</ol>
          <div class="prog" role="group" aria-label="Jump to step">
            ${STEPS.map((n) => `<button type="button" data-goto="${n}" aria-label="Step ${n}"></button>`).join("")}
          </div>
        </div>
      </div>
    </section>`;
  const root = document.getElementById("how")!;
  document.querySelector("header nav")!.getBoundingClientRect = () => ({ height: navHeight }) as DOMRect;
  const journey = document.querySelector("[data-journey]");
  if (journey) journey.getBoundingClientRect = () => ({ height: journeyHeight }) as DOMRect;
  root.querySelector<HTMLElement>(".how-track")!.getBoundingClientRect = () =>
    ({ top: trackTop, height: TRACK_H, bottom: trackTop + TRACK_H }) as DOMRect;
  return root;
}

function scrollTick() {
  window.dispatchEvent(new Event("scroll"));
}

beforeEach(() => {
  allowed = true;
  gateSubs = [];
  gate = {
    allowed: () => allowed,
    userPaused: () => !allowed,
    setUserPaused: () => {},
    subscribe: (fn) => {
      gateSubs.push(fn);
      return () => {
        gateSubs = gateSubs.filter((f) => f !== fn);
      };
    },
  };
  trackTop = 0;
  wide = true;
  mqListeners = [];
  navHeight = 0;
  journeyHeight = null;
  ioCallback = null;
  teardown = null;
  scrollTo = vi.fn();
  Object.defineProperty(window, "innerHeight", { configurable: true, value: VH });
  Object.defineProperty(window, "scrollY", { configurable: true, value: 0 });
  window.scrollTo = scrollTo as unknown as typeof window.scrollTo;
  window.requestAnimationFrame = ((cb: FrameRequestCallback) => {
    cb(0);
    return 0;
  }) as typeof window.requestAnimationFrame;
  window.matchMedia = ((query: string) => ({
    media: query,
    get matches() {
      return query.includes("min-width: 768px") ? wide : false;
    },
    addEventListener: (_: string, fn: () => void) => mqListeners.push(fn),
    removeEventListener: (_: string, fn: () => void) => {
      mqListeners = mqListeners.filter((f) => f !== fn);
    },
  })) as unknown as typeof window.matchMedia;
  (globalThis as unknown as { IntersectionObserver: unknown }).IntersectionObserver = class {
    constructor(cb: (entries: Array<{ isIntersecting: boolean; intersectionRatio: number }>) => void) {
      ioCallback = cb;
    }
    observe() {}
    disconnect() {}
  };
});

afterEach(() => {
  teardown?.();
  document.body.innerHTML = "";
});

describe("initHowSteps", () => {
  it("marks the active step", () => {
    const root = build();
    trackTop = -0.8 * RANGE;
    teardown = initHowSteps(root, gate);
    scrollTick();
    expect(root.querySelector('li.step[data-step="3"]')!.classList.contains("on")).toBe(true);
    expect(root.querySelector('li.step[data-step="2"]')!.classList.contains("on")).toBe(false);
    expect(root.querySelector('button[data-goto="3"]')!.getAttribute("aria-current")).toBe("step");
    expect(root.querySelector('button[data-goto="2"]')!.hasAttribute("aria-current")).toBe(false);
  });

  it("enhances: adds .is-enhanced and sets --stack-h to the nav alone when there is no journey bar", () => {
    navHeight = 62;
    const root = build();
    teardown = initHowSteps(root, gate);
    expect(root.classList.contains("is-enhanced")).toBe(true);
    expect(root.style.getPropertyValue("--stack-h")).toBe("62px");
  });

  it("sets --stack-h to the nav plus the journey bar when the bar is on the page", () => {
    navHeight = 64;
    journeyHeight = 45;
    const root = build();
    teardown = initHowSteps(root, gate);
    expect(root.style.getPropertyValue("--stack-h")).toBe("109px");
  });

  it("re-measures the stack on resize", () => {
    navHeight = 62;
    journeyHeight = 45;
    const root = build();
    teardown = initHowSteps(root, gate);
    navHeight = 48;
    journeyHeight = 40;
    window.dispatchEvent(new Event("resize"));
    expect(root.style.getPropertyValue("--stack-h")).toBe("88px");
  });

  it("starts the first step under the whole stack, not the nav alone", () => {
    navHeight = 64;
    journeyHeight = 45;
    const root = build();
    // The track's top is exactly at the bottom of the nav + bar: progress 0.
    trackTop = 109;
    teardown = initHowSteps(root, gate);
    scrollTick();
    expect(root.querySelector('li.step[data-step="1"]')!.classList.contains("on")).toBe(true);
    // Scrolled two thirds of the way through the run: step 3.
    trackTop = 109 - (2 / 3 + 0.01) * (TRACK_H - VH + 109);
    scrollTick();
    expect(root.querySelector('li.step[data-step="3"]')!.classList.contains("on")).toBe(true);
    // With the nav alone that same position would still be step 2.
    expect(stepFor(progressOf(trackTop, TRACK_H, VH, 64), 3)).toBe(3);
    trackTop = 109 - 0.3 * (TRACK_H - VH + 109);
    scrollTick();
    expect(root.querySelector('li.step[data-step="1"]')!.classList.contains("on")).toBe(true);
  });

  it("follows the 768px query as it changes", () => {
    const root = build();
    trackTop = -0.8 * RANGE;
    teardown = initHowSteps(root, gate);
    expect(root.classList.contains("is-enhanced")).toBe(true);
    expect(root.querySelector(".on")).not.toBeNull();
    wide = false;
    for (const fn of mqListeners) fn();
    expect(root.classList.contains("is-enhanced")).toBe(false);
    expect(root.querySelector(".on")).toBeNull();
    wide = true;
    for (const fn of mqListeners) fn();
    expect(root.classList.contains("is-enhanced")).toBe(true);
    expect(root.querySelector('li.step[data-step="3"]')!.classList.contains("on")).toBe(true);
  });

  it.each(STEPS)("pip %i scrolls to inside its step band, under a 56px nav", (n) => {
    navHeight = 56;
    const root = build();
    trackTop = -200;
    Object.defineProperty(window, "scrollY", { configurable: true, value: 1000 });
    teardown = initHowSteps(root, gate);
    root.querySelector<HTMLButtonElement>(`button[data-goto="${n}"]`)!.click();
    expect(scrollTo).toHaveBeenCalledTimes(1);
    const { top, behavior } = scrollTo.mock.calls[0]![0] as { top: number; behavior: string };
    // The track's top once scrolled there, read back through progressOf.
    const landedTop = trackTop - (top - 1000);
    expect(stepFor(progressOf(landedTop, TRACK_H, VH, 56))).toBe(n);
    expect(behavior).toBe("smooth");
  });

  it.each(STEPS)("pip %i lands inside its band under the nav and the journey bar", (n) => {
    navHeight = 64;
    journeyHeight = 45;
    const root = build();
    trackTop = -200;
    Object.defineProperty(window, "scrollY", { configurable: true, value: 1000 });
    teardown = initHowSteps(root, gate);
    root.querySelector<HTMLButtonElement>(`button[data-goto="${n}"]`)!.click();
    const { top } = scrollTo.mock.calls[0]![0] as { top: number };
    const landedTop = trackTop - (top - 1000);
    expect(stepFor(progressOf(landedTop, TRACK_H, VH, 109))).toBe(n);
  });

  it("scrolls with behavior instant when the gate does not allow motion", () => {
    const root = build();
    allowed = false;
    teardown = initHowSteps(root, gate);
    root.querySelector<HTMLButtonElement>('button[data-goto="3"]')!.click();
    expect(scrollTo.mock.calls[0]![0].behavior).toBe("instant");
  });

  it("focus on a link inside an unlit step brings that step forward", () => {
    navHeight = 56;
    const root = build();
    trackTop = -200;
    Object.defineProperty(window, "scrollY", { configurable: true, value: 1000 });
    teardown = initHowSteps(root, gate);
    expect(root.querySelector('li.step[data-step="1"]')!.classList.contains("on")).toBe(true);
    root.querySelector<HTMLAnchorElement>('li.step[data-step="3"] a')!.focus();
    expect(scrollTo).toHaveBeenCalledTimes(1);
    const { top, behavior } = scrollTo.mock.calls[0]![0] as { top: number; behavior: string };
    expect(stepFor(progressOf(trackTop - (top - 1000), TRACK_H, VH, 56))).toBe(3);
    expect(behavior).toBe("smooth");
  });

  it("focus into the step that is already lit scrolls nothing, and focus with motion off jumps", () => {
    const root = build();
    teardown = initHowSteps(root, gate);
    root.querySelector<HTMLAnchorElement>('li.step[data-step="1"] a')!.focus();
    expect(scrollTo).not.toHaveBeenCalled();
    allowed = false;
    root.querySelector<HTMLAnchorElement>('li.step[data-step="2"] a')!.focus();
    expect(scrollTo.mock.calls[0]![0].behavior).toBe("instant");
  });

  it("does not move the page on focus when the layout is not enhanced", () => {
    wide = false;
    const root = build();
    teardown = initHowSteps(root, gate);
    root.querySelector<HTMLAnchorElement>('li.step[data-step="3"] a')!.focus();
    expect(scrollTo).not.toHaveBeenCalled();
  });

  it("plays the diagrams only while the stage is visible", () => {
    const root = build();
    teardown = initHowSteps(root, gate);
    const stage = root.querySelector(".how-stage")!;
    expect(stage.classList.contains("is-playing")).toBe(false);
    ioCallback!([{ isIntersecting: true, intersectionRatio: 1 }]);
    expect(stage.classList.contains("is-playing")).toBe(true);
    ioCallback!([{ isIntersecting: false, intersectionRatio: 0 }]);
    expect(stage.classList.contains("is-playing")).toBe(false);
  });

  it("does nothing below 768px", () => {
    wide = false;
    const root = build();
    teardown = initHowSteps(root, gate);
    scrollTick();
    expect(root.classList.contains("is-enhanced")).toBe(false);
    expect(root.querySelector(".on")).toBeNull();
  });

  it("removes its listeners and enhancement on dispose", () => {
    const root = build();
    teardown = initHowSteps(root, gate);
    teardown();
    teardown = null;
    expect(root.classList.contains("is-enhanced")).toBe(false);
    trackTop = -0.8 * RANGE;
    scrollTick();
    expect(root.querySelector(".on")).toBeNull();
  });
});
