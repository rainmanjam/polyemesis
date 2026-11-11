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
  it("does not divide by zero when the track is no taller than the viewport", () => {
    expect(progressOf(-10, 900, 900)).toBe(0);
  });
});

describe("stepFor", () => {
  it("maps fifths", () => {
    expect(stepFor(0)).toBe(1);
    expect(stepFor(0.2)).toBe(2);
    expect(stepFor(0.4)).toBe(3);
    expect(stepFor(0.99)).toBe(5);
    expect(stepFor(1)).toBe(5);
  });
});

let allowed: boolean;
let gateSubs: Array<(a: boolean) => void>;
let gate: MotionGate;
let trackTop: number;
let ioCallback: ((entries: Array<{ isIntersecting: boolean }>) => void) | null;
let wide: boolean;
let scrollTo: ReturnType<typeof vi.fn>;
let teardown: (() => void) | null;

const STEPS = [1, 2, 3, 4, 5];

function build(): HTMLElement {
  document.body.innerHTML = `
    <header><nav style="height:56px"></nav></header>
    <section id="how">
      <div class="how-track">
        <div class="how-stage">
          <ol class="steps">${STEPS.map((n) => `<li class="step" data-step="${n}"><h3>${n}</h3></li>`).join("")}</ol>
          <div class="prog" role="group" aria-label="Jump to step">
            ${STEPS.map((n) => `<button type="button" data-goto="${n}" aria-label="Step ${n}"></button>`).join("")}
          </div>
          <div class="dg" data-dg>
            <ul class="src">
              <li class="si" data-src="0" data-on="1 2 5"></li>
              <li class="si" data-src="1" data-on="1 3 4 5"></li>
            </ul>
            <div class="hub" data-on="2 3 4 5"></div>
          </div>
        </div>
      </div>
    </section>`;
  const root = document.getElementById("how")!;
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
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia;
  (globalThis as unknown as { IntersectionObserver: unknown }).IntersectionObserver = class {
    constructor(cb: (entries: Array<{ isIntersecting: boolean }>) => void) {
      ioCallback = cb;
    }
    observe() {}
    disconnect() {}
  };
});

afterEach(() => {
  teardown?.();
  document.body.innerHTML = "";
  document.documentElement.style.removeProperty("--nav-h");
});

describe("initHowSteps", () => {
  it("marks the active step and hides parts not in it", () => {
    const root = build();
    trackTop = -0.4 * RANGE;
    teardown = initHowSteps(root, gate);
    scrollTick();
    expect(root.querySelector('li.step[data-step="3"]')!.classList.contains("on")).toBe(true);
    expect(root.querySelector('li.step[data-step="2"]')!.classList.contains("on")).toBe(false);
    expect(root.querySelector(".dg")!.getAttribute("data-step")).toBe("3");
    expect(root.querySelector('.si[data-src="0"]')!.classList.contains("off")).toBe(true);
    expect(root.querySelector('.si[data-src="1"]')!.classList.contains("off")).toBe(false);
    expect(root.querySelector('button[data-goto="3"]')!.getAttribute("aria-current")).toBe("step");
    expect(root.querySelector('button[data-goto="2"]')!.hasAttribute("aria-current")).toBe(false);
  });

  it("enhances: adds .is-enhanced and measures --nav-h from header nav", () => {
    const root = build();
    document.querySelector("header nav")!.getBoundingClientRect = () => ({ height: 62 }) as DOMRect;
    teardown = initHowSteps(root, gate);
    expect(root.classList.contains("is-enhanced")).toBe(true);
    expect(root.style.getPropertyValue("--nav-h")).toBe("62px");
  });

  it.each(STEPS)("pip %i scrolls to inside its step band", (n) => {
    const root = build();
    trackTop = -200;
    Object.defineProperty(window, "scrollY", { configurable: true, value: 1000 });
    teardown = initHowSteps(root, gate);
    root.querySelector<HTMLButtonElement>(`button[data-goto="${n}"]`)!.click();
    expect(scrollTo).toHaveBeenCalledTimes(1);
    const { top, behavior } = scrollTo.mock.calls[0]![0] as { top: number; behavior: string };
    // Scroll position that puts the track at this progress.
    const progress = (top - (1000 + trackTop)) / RANGE;
    expect(stepFor(progress)).toBe(n);
    expect(behavior).toBe("smooth");
  });

  it("scrolls with behavior auto when the gate does not allow motion", () => {
    const root = build();
    allowed = false;
    teardown = initHowSteps(root, gate);
    root.querySelector<HTMLButtonElement>('button[data-goto="4"]')!.click();
    expect(scrollTo.mock.calls[0]![0].behavior).toBe("auto");
  });

  it("plays the steps diagram only while visible", () => {
    const root = build();
    teardown = initHowSteps(root, gate);
    const dg = root.querySelector(".dg")!;
    expect(dg.classList.contains("is-playing")).toBe(false);
    ioCallback!([{ isIntersecting: true }]);
    expect(dg.classList.contains("is-playing")).toBe(true);
    ioCallback!([{ isIntersecting: false }]);
    expect(dg.classList.contains("is-playing")).toBe(false);
  });

  it("does nothing below 768px", () => {
    wide = false;
    const root = build();
    teardown = initHowSteps(root, gate);
    scrollTick();
    expect(root.classList.contains("is-enhanced")).toBe(false);
    expect(root.querySelector(".dg")!.hasAttribute("data-step")).toBe(false);
    expect(root.querySelector(".on")).toBeNull();
    expect(root.querySelector(".off")).toBeNull();
  });

  it("removes its listeners and enhancement on dispose", () => {
    const root = build();
    teardown = initHowSteps(root, gate);
    teardown();
    teardown = null;
    expect(root.classList.contains("is-enhanced")).toBe(false);
    trackTop = -0.4 * RANGE;
    scrollTick();
    expect(root.querySelector(".on")).toBeNull();
  });
});
