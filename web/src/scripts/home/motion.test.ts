// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createMotionGate, whileVisible } from "./motion";

type MQL = { matches: boolean; listeners: Array<(e: { matches: boolean }) => void> };
let mql: MQL;
let ioCallback: ((entries: Array<{ isIntersecting: boolean }>) => void) | null;
let ioDisconnect: () => void;

function setReduced(matches: boolean) {
  mql.matches = matches;
  for (const l of [...mql.listeners]) l({ matches });
}
function intersect(isIntersecting: boolean) {
  ioCallback!([{ isIntersecting }]);
}
function setVisibility(state: "visible" | "hidden") {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => state });
  document.dispatchEvent(new Event("visibilitychange"));
}

beforeEach(() => {
  mql = { matches: false, listeners: [] };
  ioCallback = null;
  ioDisconnect = vi.fn() as unknown as () => void;
  window.matchMedia = ((query: string) => ({
    media: query,
    get matches() {
      return mql.matches;
    },
    addEventListener: (_: string, fn: (e: { matches: boolean }) => void) => mql.listeners.push(fn),
    removeEventListener: (_: string, fn: (e: { matches: boolean }) => void) => {
      mql.listeners = mql.listeners.filter((l) => l !== fn);
    },
  })) as unknown as typeof window.matchMedia;
  (globalThis as unknown as { IntersectionObserver: unknown }).IntersectionObserver = class {
    constructor(cb: (entries: Array<{ isIntersecting: boolean }>) => void) {
      ioCallback = cb;
    }
    observe() {}
    unobserve() {}
    disconnect() {
      ioDisconnect();
    }
  };
  document.documentElement.classList.remove("motion-on");
  setVisibility("visible");
});

afterEach(() => {
  document.documentElement.classList.remove("motion-on");
});

describe("createMotionGate", () => {
  it("adds motion-on when motion is allowed", () => {
    const gate = createMotionGate(window);
    expect(gate.allowed()).toBe(true);
    expect(document.documentElement.classList.contains("motion-on")).toBe(true);
  });

  it("honours prefers-reduced-motion at start and on change", () => {
    mql.matches = true;
    const gate = createMotionGate(window);
    expect(gate.allowed()).toBe(false);
    expect(document.documentElement.classList.contains("motion-on")).toBe(false);
    setReduced(false);
    expect(gate.allowed()).toBe(true);
    expect(document.documentElement.classList.contains("motion-on")).toBe(true);
  });

  it("user pause wins over no-preference and notifies subscribers", () => {
    const gate = createMotionGate(window);
    const fn = vi.fn();
    gate.subscribe(fn);
    gate.setUserPaused(true);
    expect(gate.userPaused()).toBe(true);
    expect(gate.allowed()).toBe(false);
    expect(fn).toHaveBeenCalledWith(false);
    expect(document.documentElement.classList.contains("motion-on")).toBe(false);
  });

  it("unsubscribe stops notifications", () => {
    const gate = createMotionGate(window);
    const fn = vi.fn();
    const off = gate.subscribe(fn);
    off();
    gate.setUserPaused(true);
    expect(fn).not.toHaveBeenCalled();
  });
});

describe("whileVisible", () => {
  it("starts on intersect and stops when hidden or paused", () => {
    const gate = createMotionGate(window);
    const el = document.createElement("div");
    const start = vi.fn();
    const stop = vi.fn();
    whileVisible(el, gate, start, stop);
    intersect(true);
    expect(start).toHaveBeenCalledTimes(1);
    setVisibility("hidden");
    expect(stop).toHaveBeenCalledTimes(1);
    setVisibility("visible");
    expect(start).toHaveBeenCalledTimes(2);
    gate.setUserPaused(true);
    expect(stop).toHaveBeenCalledTimes(2);
  });

  it("never starts under reduced motion", () => {
    mql.matches = true;
    const gate = createMotionGate(window);
    const start = vi.fn();
    whileVisible(document.createElement("div"), gate, start, vi.fn());
    intersect(true);
    expect(start).not.toHaveBeenCalled();
  });

  it("stops when the element scrolls out and the disposer disconnects", () => {
    const gate = createMotionGate(window);
    const start = vi.fn();
    const stop = vi.fn();
    const dispose = whileVisible(document.createElement("div"), gate, start, stop);
    intersect(true);
    intersect(false);
    expect(stop).toHaveBeenCalledTimes(1);
    intersect(true);
    dispose();
    expect(stop).toHaveBeenCalledTimes(2);
    expect(ioDisconnect).toHaveBeenCalled();
    gate.setUserPaused(true);
    gate.setUserPaused(false);
    expect(start).toHaveBeenCalledTimes(2);
  });
});
