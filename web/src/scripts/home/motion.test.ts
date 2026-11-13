// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createMotionGate, whileVisible } from "./motion";

type MQL = { matches: boolean; listeners: Array<(e: { matches: boolean }) => void> };
let mql: MQL;
type FakeEntry = {
  isIntersecting: boolean;
  intersectionRatio: number;
  boundingClientRect?: { height: number };
  intersectionRect?: { height: number };
  rootBounds?: { height: number } | null;
};
let ioCallback: ((entries: FakeEntry[]) => void) | null;
let ioOptions: IntersectionObserverInit | undefined;
let ioDisconnect: () => void;
const original = {
  matchMedia: window.matchMedia,
  IntersectionObserver: (globalThis as unknown as { IntersectionObserver: unknown }).IntersectionObserver,
  visibility: Object.getOwnPropertyDescriptor(document, "visibilityState"),
};

function setReduced(matches: boolean) {
  mql.matches = matches;
  for (const l of [...mql.listeners]) l({ matches });
}
function intersect(isIntersecting: boolean, intersectionRatio = isIntersecting ? 1 : 0, extra: Partial<FakeEntry> = {}) {
  ioCallback!([
    {
      isIntersecting,
      intersectionRatio,
      boundingClientRect: { height: 100 },
      intersectionRect: { height: 100 * intersectionRatio },
      rootBounds: { height: 1000 },
      ...extra,
    },
  ]);
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
    constructor(cb: (entries: FakeEntry[]) => void, options?: IntersectionObserverInit) {
      ioCallback = cb;
      ioOptions = options;
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
  window.matchMedia = original.matchMedia;
  (globalThis as unknown as { IntersectionObserver: unknown }).IntersectionObserver = original.IntersectionObserver;
  if (original.visibility) Object.defineProperty(document, "visibilityState", original.visibility);
  else delete (document as unknown as { visibilityState?: unknown }).visibilityState;
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
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

  it("resuming puts motion-on back and notifies true", () => {
    const gate = createMotionGate(window);
    const fn = vi.fn();
    gate.subscribe(fn);
    gate.setUserPaused(true);
    gate.setUserPaused(false);
    expect(gate.allowed()).toBe(true);
    expect(fn).toHaveBeenLastCalledWith(true);
    expect(document.documentElement.classList.contains("motion-on")).toBe(true);
  });

  it("a reduced-motion flip notifies subscribers both ways, and pause still wins after it", () => {
    const gate = createMotionGate(window);
    const fn = vi.fn();
    gate.subscribe(fn);
    setReduced(true);
    expect(fn).toHaveBeenLastCalledWith(false);
    setReduced(false);
    expect(fn).toHaveBeenLastCalledWith(true);
    gate.setUserPaused(true);
    setReduced(true);
    setReduced(false);
    expect(gate.allowed()).toBe(false);
    expect(fn).toHaveBeenCalledTimes(3);
  });

  it("a throwing subscriber does not stop the rest, and its error is reported", () => {
    const reported = vi.fn();
    vi.stubGlobal("reportError", reported);
    const gate = createMotionGate(window);
    const after = vi.fn();
    gate.subscribe(() => {
      throw new Error("boom");
    });
    gate.subscribe(after);
    gate.setUserPaused(true);
    expect(after).toHaveBeenCalledWith(false);
    expect(reported).toHaveBeenCalledTimes(1);
    expect((reported.mock.calls[0]![0] as Error).message).toBe("boom");
  });

  it("dispose removes the media-query listener", () => {
    const gate = createMotionGate(window);
    expect(mql.listeners).toHaveLength(1);
    gate.dispose();
    expect(mql.listeners).toHaveLength(0);
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

  it("honours its threshold: a sliver on screen is not visible enough", () => {
    const gate = createMotionGate(window);
    const start = vi.fn();
    const stop = vi.fn();
    whileVisible(document.createElement("div"), gate, start, stop, 0.5);
    expect(ioOptions?.threshold).toEqual(expect.arrayContaining([0, 0.5]));
    intersect(true, 0.1);
    expect(start).not.toHaveBeenCalled();
    intersect(true, 0.5);
    expect(start).toHaveBeenCalledTimes(1);
    intersect(true, 0.3);
    expect(stop).toHaveBeenCalledTimes(1);
  });

  it("a block taller than the viewport counts once it fills the threshold of the viewport height", () => {
    const gate = createMotionGate(window);
    const start = vi.fn();
    const stop = vi.fn();
    whileVisible(document.createElement("div"), gate, start, stop, 0.5);
    const tall = (inter: number) => ({
      boundingClientRect: { height: 3000 },
      intersectionRect: { height: inter },
      rootBounds: { height: 800 },
    });
    intersect(true, 150 / 3000, tall(150)); // 150 of 800 px: not yet
    expect(start).not.toHaveBeenCalled();
    intersect(true, 400 / 3000, tall(400)); // half the viewport height
    expect(start).toHaveBeenCalledTimes(1);
    intersect(true, 100 / 3000, tall(100));
    expect(stop).toHaveBeenCalledTimes(1);
  });

  it("a short block does not take the tall-block rule", () => {
    const gate = createMotionGate(window);
    const start = vi.fn();
    whileVisible(document.createElement("div"), gate, start, vi.fn(), 0.5);
    intersect(true, 0.2, {
      boundingClientRect: { height: 300 },
      intersectionRect: { height: 60 },
      rootBounds: { height: 1000 },
    });
    expect(start).not.toHaveBeenCalled();
  });
});
