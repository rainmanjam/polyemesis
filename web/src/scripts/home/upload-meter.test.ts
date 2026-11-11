// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { MotionGate } from "./motion";
import { MAX_PLATFORMS, START_PLATFORMS, TICK_MS, UPLOAD_LIMIT, initUploadMeter, uploadFor } from "./upload-meter";

let ioCallback: ((entries: Array<{ isIntersecting: boolean }>) => void) | null;

function fakeGate(initial = true) {
  let allowed = initial;
  const subs = new Set<(a: boolean) => void>();
  const gate: MotionGate = {
    allowed: () => allowed,
    userPaused: () => !allowed,
    setUserPaused: () => {},
    subscribe(fn) {
      subs.add(fn);
      return () => subs.delete(fn);
    },
  };
  return {
    gate,
    set(a: boolean) {
      allowed = a;
      for (const fn of [...subs]) fn(a);
    },
  };
}

function build(): HTMLElement {
  document.body.innerHTML = `
    <section data-upload-meter>
      <p>Platforms you stream to: <span data-pcount>3</span></p>
      <div class="mt"><div class="bar"><i data-kind="relay" style="--k:.2"></i><b class="lim"></b></div><span class="val" data-val="relay">1×</span></div>
      <div class="mt"><div class="bar"><i data-kind="plugin" class="over" style="--k:.6"></i><b class="lim"></b></div><span class="val" data-val="plugin">3×</span></div>
      <div class="mt"><div class="bar"><i data-kind="scripts" style="--k:.2"></i><b class="lim"></b></div><span class="val" data-val="scripts">1×</span></div>
      <div class="mt"><div class="bar"><i data-kind="polyemesis" style="--k:.2"></i><b class="lim"></b></div><span class="val" data-val="polyemesis">1×</span></div>
    </section>`;
  return document.querySelector("[data-upload-meter]") as HTMLElement;
}

const count = (root: HTMLElement) => root.querySelector("[data-pcount]")!.textContent;
const plugin = (root: HTMLElement) => root.querySelector('i[data-kind="plugin"]') as HTMLElement;
const val = (root: HTMLElement, k: string) => root.querySelector(`[data-val="${k}"]`)!.textContent;
const intersect = (v: boolean) => ioCallback!([{ isIntersecting: v }]);

beforeEach(() => {
  vi.useFakeTimers();
  ioCallback = null;
  (globalThis as unknown as { IntersectionObserver: unknown }).IntersectionObserver = class {
    constructor(cb: (entries: Array<{ isIntersecting: boolean }>) => void) {
      ioCallback = cb;
    }
    observe() {}
    unobserve() {}
    disconnect() {}
  };
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "visible" });
});

afterEach(() => {
  vi.useRealTimers();
  document.body.innerHTML = "";
});

describe("uploadFor", () => {
  it("multiplies only for the plugin", () => {
    expect(uploadFor("plugin", 4)).toBe(4);
    for (const k of ["relay", "scripts", "polyemesis"] as const) {
      for (const n of [1, 3, 5]) expect(uploadFor(k, n)).toBe(1);
    }
  });
});

describe("initUploadMeter", () => {
  it("ticks 1→5 then wraps while allowed", () => {
    const root = build();
    initUploadMeter(root, fakeGate().gate);
    expect(count(root)).toBe(String(START_PLATFORMS));
    intersect(true);
    expect(count(root)).toBe("1");
    const seen: string[] = [];
    for (let i = 0; i < MAX_PLATFORMS; i++) {
      vi.advanceTimersByTime(TICK_MS);
      seen.push(count(root)!);
    }
    expect(seen).toEqual(["2", "3", "4", "5", "1"]);
  });

  it("marks the plugin bar over the limit past UPLOAD_LIMIT", () => {
    const root = build();
    initUploadMeter(root, fakeGate().gate);
    intersect(true);
    expect(plugin(root).classList.contains("over")).toBe(false);
    expect(val(root, "plugin")).toBe("1×");
    for (let n = 2; n <= MAX_PLATFORMS; n++) {
      vi.advanceTimersByTime(TICK_MS);
      expect(val(root, "plugin")).toBe(`${n}×`);
      expect(plugin(root).classList.contains("over")).toBe(n > UPLOAD_LIMIT);
      expect(plugin(root).style.getPropertyValue("--k")).toBe(String(n / MAX_PLATFORMS));
    }
    expect(root.querySelector('i[data-kind="polyemesis"]')!.classList.contains("over")).toBe(false);
    expect(val(root, "polyemesis")).toBe("1×");
  });

  it("stays at 3 and never starts under a paused gate", () => {
    const root = build();
    initUploadMeter(root, fakeGate(false).gate);
    intersect(true);
    vi.advanceTimersByTime(TICK_MS * 10);
    expect(count(root)).toBe("3");
    expect(val(root, "plugin")).toBe("3×");
    expect(plugin(root).classList.contains("over")).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("stops when the gate pauses mid-run and restores 3 with the plugin over", () => {
    const root = build();
    const g = fakeGate();
    initUploadMeter(root, g.gate);
    intersect(true);
    vi.advanceTimersByTime(TICK_MS);
    expect(count(root)).toBe("2");
    g.set(false);
    expect(count(root)).toBe("3");
    expect(val(root, "plugin")).toBe("3×");
    expect(plugin(root).classList.contains("over")).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
    vi.advanceTimersByTime(TICK_MS * 5);
    expect(count(root)).toBe("3");
  });

  it("disposer stops the timer", () => {
    const root = build();
    const dispose = initUploadMeter(root, fakeGate().gate);
    intersect(true);
    dispose();
    expect(vi.getTimerCount()).toBe(0);
    expect(count(root)).toBe("3");
  });
});
