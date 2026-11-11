// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initFailover, STAGES } from "./failover";
import type { MotionGate } from "./motion";

let ioCallback: ((entries: Array<{ isIntersecting: boolean }>) => void) | null;

function makeGate(allowed: boolean): MotionGate {
  return {
    allowed: () => allowed,
    userPaused: () => !allowed,
    setUserPaused: () => {},
    subscribe: () => () => {},
  };
}

function fixture(): HTMLElement {
  document.body.innerHTML = `
    <section data-failover>
      <ol>${STAGES.map((s, i) => `<li><button type="button" data-stage="${i}">${s.label}</button></li>`).join("")}</ol>
      <button type="button" data-play>Pull the cable</button>
      <div data-frame data-state="live"><span data-enc></span></div>
      <ul><li data-dest></li><li data-dest></li><li data-dest></li></ul>
      <ol data-log aria-live="polite" aria-label="Event log, illustrative"></ol>
    </section>`;
  return document.querySelector<HTMLElement>("[data-failover]")!;
}

const frame = () => document.querySelector<HTMLElement>("[data-frame]")!;
const state = () => frame().dataset.state;
const logLines = () => [...document.querySelectorAll("[data-log] li")].map((l) => l.textContent);

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
});

afterEach(() => {
  vi.useRealTimers();
  document.body.innerHTML = "";
});

describe("STAGES", () => {
  it("matches the labels, durations and copy", () => {
    expect(STAGES.map((s) => [s.id, s.label, s.ms])).toEqual([
      ["live", "Live", 2600],
      ["drop", "Encoder drops", 1800],
      ["slate", "Slate on air", 3400],
      ["recovered", "Recovered", 2400],
    ]);
    expect(STAGES.map((s) => s.encoder)).toEqual([
      "Encoder connected",
      "Encoder lost",
      "Encoder lost, slate holding",
      "Encoder back",
    ]);
    expect(STAGES.map((s) => s.log)).toEqual([
      "Encoder connected. All destinations live.",
      "Ingest lost.",
      "Slate on air for Twitch, YouTube and Kick.",
      "Encoder back. Programme restored.",
    ]);
  });
});

describe("initFailover", () => {
  it("autoplays once when 50% visible and motion allowed", () => {
    const f = initFailover(fixture(), makeGate(true));
    expect(state()).toBe("live");
    ioCallback!([{ isIntersecting: true }]);
    const seen = [state()];
    for (const s of STAGES.slice(0, -1)) {
      vi.advanceTimersByTime(s.ms + (s.id === "live" ? 700 : 0));
      seen.push(state());
    }
    expect(seen).toEqual(["live", "drop", "slate", "recovered"]);
    vi.advanceTimersByTime(20000);
    expect(state()).toBe("recovered");
    // leaving and re-entering does not replay
    ioCallback!([{ isIntersecting: false }]);
    ioCallback!([{ isIntersecting: true }]);
    vi.advanceTimersByTime(20000);
    expect(state()).toBe("recovered");
    f.destroy();
  });

  it("does not autoplay under a paused gate, but Pull the cable plays it", () => {
    const f = initFailover(fixture(), makeGate(false));
    ioCallback!([{ isIntersecting: true }]);
    vi.advanceTimersByTime(20000);
    expect(state()).toBe("live");
    document.querySelector<HTMLButtonElement>("[data-play]")!.click();
    expect(state()).toBe("drop");
    vi.advanceTimersByTime(STAGES[1].ms);
    expect(state()).toBe("slate");
    vi.advanceTimersByTime(STAGES[2].ms);
    expect(state()).toBe("recovered");
    vi.advanceTimersByTime(20000);
    expect(state()).toBe("recovered");
    f.destroy();
  });

  it("stage buttons jump to a stage and set aria-current=step", () => {
    const f = initFailover(fixture(), makeGate(false));
    const btn = (i: number) => document.querySelector<HTMLButtonElement>(`button[data-stage="${i}"]`)!;
    btn(2).click();
    expect(state()).toBe("slate");
    expect(btn(2).getAttribute("aria-current")).toBe("step");
    expect(btn(0).hasAttribute("aria-current")).toBe(false);
    expect(document.querySelector("[data-enc]")!.textContent).toBe("Encoder lost, slate holding");
    expect([...document.querySelectorAll("[data-dest]")].map((d) => d.textContent)).toEqual(["slate", "slate", "slate"]);
    btn(3).click();
    expect(btn(2).hasAttribute("aria-current")).toBe(false);
    expect(btn(3).getAttribute("aria-current")).toBe("step");
    f.destroy();
  });

  it("a manual jump stops a running autoplay", () => {
    const f = initFailover(fixture(), makeGate(true));
    ioCallback!([{ isIntersecting: true }]);
    vi.advanceTimersByTime(700 + STAGES[0].ms);
    expect(state()).toBe("drop");
    document.querySelector<HTMLButtonElement>('button[data-stage="0"]')!.click();
    vi.advanceTimersByTime(20000);
    expect(state()).toBe("live");
    f.destroy();
  });

  it("appends one log line per stage, as text", () => {
    const f = initFailover(fixture(), makeGate(false));
    expect(logLines()).toEqual([STAGES[0].log]);
    f.show(2);
    expect(logLines()).toEqual(STAGES.slice(0, 3).map((s) => s.log));
    f.show(1);
    expect(logLines()).toEqual(STAGES.slice(0, 2).map((s) => s.log));
    expect(document.querySelector("[data-log] li *")).toBeNull();
    f.destroy();
  });

  it("destroy stops a running sequence", () => {
    const f = initFailover(fixture(), makeGate(false));
    f.play();
    f.destroy();
    vi.advanceTimersByTime(20000);
    expect(state()).toBe("drop");
  });
});
