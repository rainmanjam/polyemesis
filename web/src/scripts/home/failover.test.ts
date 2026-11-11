// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initFailover, STAGES } from "./failover";
import type { MotionGate } from "./motion";

let ioCallback: ((entries: Array<{ isIntersecting: boolean }>) => void) | null;

/** A gate whose answer can change; subscribers hear it, as with the real one. */
function fakeGate(initial: boolean) {
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
const makeGate = (allowed: boolean) => fakeGate(allowed).gate;

function fixture(): HTMLElement {
  document.body.innerHTML = `
    <section data-failover>
      <div data-fo-controls hidden><ol>${STAGES.map((s, i) => `<li><button type="button" data-stage="${i}">${s.label}</button></li>`).join("")}</ol>
      <button type="button" data-play>Pull the cable</button></div>
      <div data-frame data-state="live"><span data-enc></span></div>
      <ul><li data-dest></li><li data-dest></li><li data-dest></li></ul>
      <ol data-log aria-live="off" aria-label="Event log, illustrative"></ol>
    </section>`;
  return document.querySelector<HTMLElement>("[data-failover]")!;
}

const frame = () => document.querySelector<HTMLElement>("[data-frame]")!;
const state = () => frame().dataset.state;
const play = () => document.querySelector<HTMLButtonElement>("[data-play]")!.click();
const stageBtn = (i: number) => document.querySelector<HTMLButtonElement>(`button[data-stage="${i}"]`)!;
const live = () => document.querySelector("[data-log]")!.getAttribute("aria-live");
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

  it("un-hides its controls, which the server renders hidden", () => {
    const root = fixture();
    expect(root.querySelector<HTMLElement>("[data-fo-controls]")!.hidden).toBe(true);
    const f = initFailover(root, makeGate(true));
    expect(root.querySelector<HTMLElement>("[data-fo-controls]")!.hidden).toBe(false);
    f.destroy();
  });

  it("with motion off, Pull the cable jumps straight to the slate and schedules nothing", () => {
    const f = initFailover(fixture(), makeGate(false));
    ioCallback!([{ isIntersecting: true }]);
    vi.advanceTimersByTime(20000);
    expect(state()).toBe("live");
    play();
    expect(state()).toBe("slate");
    expect(document.querySelector("[data-enc]")!.textContent).toBe("Encoder lost, slate holding");
    expect(vi.getTimerCount()).toBe(0);
    vi.advanceTimersByTime(20000);
    expect(state()).toBe("slate");
    f.destroy();
  });

  it("with motion off, the stage buttons step at once and schedule nothing", () => {
    const f = initFailover(fixture(), makeGate(false));
    stageBtn(1).click();
    expect(state()).toBe("drop");
    stageBtn(3).click();
    expect(state()).toBe("recovered");
    expect(vi.getTimerCount()).toBe(0);
    f.destroy();
  });

  it("Pause mid-run restores live and clears the timers", () => {
    const g = fakeGate(true);
    const f = initFailover(fixture(), g.gate);
    play();
    vi.advanceTimersByTime(STAGES[1].ms);
    expect(state()).toBe("slate");
    g.set(false);
    expect(state()).toBe("live");
    expect(vi.getTimerCount()).toBe(0);
    vi.advanceTimersByTime(20000);
    expect(state()).toBe("live");
    f.destroy();
  });

  it("Pause stops a visitor's run even when the frame was never half on screen", () => {
    const g = fakeGate(true);
    const f = initFailover(fixture(), g.gate);
    play(); // no intersection reported: whileVisible never started
    expect(state()).toBe("drop");
    g.set(false);
    expect(state()).toBe("live");
    expect(vi.getTimerCount()).toBe(0);
    f.destroy();
  });

  it("scrolling away mid-run restores live, and the visitor's run does not replay", () => {
    const f = initFailover(fixture(), makeGate(true));
    ioCallback!([{ isIntersecting: true }]);
    play();
    vi.advanceTimersByTime(STAGES[1].ms);
    expect(state()).toBe("slate");
    ioCallback!([{ isIntersecting: false }]);
    expect(state()).toBe("live");
    expect(vi.getTimerCount()).toBe(0);
    ioCallback!([{ isIntersecting: true }]);
    vi.advanceTimersByTime(20000);
    expect(state()).toBe("live");
    f.destroy();
  });

  it("announces visitor-started steps only, never autoplay", () => {
    const f = initFailover(fixture(), makeGate(true));
    expect(live()).toBe("off");
    ioCallback!([{ isIntersecting: true }]);
    vi.advanceTimersByTime(700 + STAGES[0].ms);
    expect(state()).toBe("drop");
    expect(live()).toBe("off");
    stageBtn(2).click();
    expect(live()).toBe("polite");
    f.destroy();
    const g = initFailover(fixture(), makeGate(true));
    play();
    expect(live()).toBe("polite");
    g.destroy();
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

  it("an interrupted autoplay resumes from the top and completes", () => {
    const f = initFailover(fixture(), makeGate(true));
    // leaves view during the lead-in: autoplay must not be lost
    ioCallback!([{ isIntersecting: true }]);
    vi.advanceTimersByTime(300);
    ioCallback!([{ isIntersecting: false }]);
    vi.advanceTimersByTime(20000);
    expect(state()).toBe("live");
    // leaves view mid-run: must not freeze on a failure stage
    ioCallback!([{ isIntersecting: true }]);
    vi.advanceTimersByTime(700 + STAGES[0].ms + STAGES[1].ms);
    expect(state()).toBe("slate");
    ioCallback!([{ isIntersecting: false }]);
    expect(state()).toBe("live");
    vi.advanceTimersByTime(20000);
    expect(state()).toBe("live");
    ioCallback!([{ isIntersecting: true }]);
    vi.advanceTimersByTime(700);
    expect(state()).toBe("live");
    vi.advanceTimersByTime(STAGES[0].ms + STAGES[1].ms + STAGES[2].ms);
    expect(state()).toBe("recovered");
    f.destroy();
  });

  it("appends a log line on forward steps without rebuilding the list", () => {
    const f = initFailover(fixture(), makeGate(false));
    const first = document.querySelector("[data-log] li");
    f.show(1);
    expect(document.querySelector("[data-log] li")).toBe(first);
    expect(logLines()).toHaveLength(2);
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
    const f = initFailover(fixture(), makeGate(true));
    f.play();
    expect(state()).toBe("drop");
    f.destroy();
    expect(vi.getTimerCount()).toBe(0);
    vi.advanceTimersByTime(20000);
    expect(state()).toBe("drop");
  });
});
