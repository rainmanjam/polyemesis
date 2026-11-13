// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { MotionGate } from "./motion";
import { clock, isoDuration } from "../../data/tour.mjs";
import { initTour } from "./tour";

type Entry = { isIntersecting: boolean; intersectionRatio: number };

let allowed: boolean;
let gateSubs: Array<(a: boolean) => void>;
let gate: MotionGate;
let ioCallback: ((entries: Entry[]) => void) | null;
let ioOptions: IntersectionObserverInit | undefined;
let paused: boolean;
let play: ReturnType<typeof vi.fn>;
let pause: ReturnType<typeof vi.fn>;
let teardown: (() => void) | null;
let visibility: DocumentVisibilityState;

function build(): { root: HTMLElement; video: HTMLVideoElement } {
  document.body.innerHTML = `
    <div data-tour>
      <video muted playsinline preload="none" controls></video>
      <ol>
        <li><button type="button" data-seek="0" disabled>intro</button></li>
        <li><button type="button" data-seek="22.2" disabled>split</button></li>
        <li><button type="button" data-seek="58.6" disabled>out</button></li>
      </ol>
    </div>`;
  const root = document.querySelector<HTMLElement>("[data-tour]")!;
  const video = root.querySelector("video")!;
  // jsdom implements none of media playback: stand in for the parts initTour uses.
  Object.defineProperty(video, "paused", { configurable: true, get: () => paused });
  video.play = play as unknown as HTMLVideoElement["play"];
  video.pause = pause as unknown as HTMLVideoElement["pause"];
  return { root, video };
}

const see = (ratio: number) => ioCallback!([{ isIntersecting: ratio > 0, intersectionRatio: ratio }]);

beforeEach(() => {
  allowed = true;
  paused = true;
  visibility = "visible";
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
  play = vi.fn(() => {
    paused = false;
    return Promise.resolve();
  });
  pause = vi.fn(() => {
    paused = true;
  });
  ioCallback = null;
  ioOptions = undefined;
  teardown = null;
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => visibility });
  (globalThis as unknown as { IntersectionObserver: unknown }).IntersectionObserver = class {
    constructor(cb: (entries: Entry[]) => void, options?: IntersectionObserverInit) {
      ioCallback = cb;
      ioOptions = options;
    }
    observe() {}
    disconnect() {}
  };
});

afterEach(() => {
  teardown?.();
  document.body.innerHTML = "";
});

describe("initTour", () => {
  it("does not play before the block is seen", () => {
    const { root } = build();
    teardown = initTour(root, gate);
    expect(play).not.toHaveBeenCalled();
  });

  it("asks to be told at half visible", () => {
    const { root } = build();
    teardown = initTour(root, gate);
    expect(ioOptions?.threshold).toContain(0.5);
  });

  it("plays at half visible when the gate allows", () => {
    const { root } = build();
    teardown = initTour(root, gate);
    see(0.5);
    expect(play).toHaveBeenCalledTimes(1);
  });

  it("does not play while less than half is visible, even though it intersects", () => {
    const { root } = build();
    teardown = initTour(root, gate);
    see(0.3);
    expect(play).not.toHaveBeenCalled();
  });

  it("does not play when the gate disallows", () => {
    allowed = false;
    const { root } = build();
    teardown = initTour(root, gate);
    see(1);
    expect(play).not.toHaveBeenCalled();
  });

  it("pauses when the gate stops allowing, and plays again when it allows", () => {
    const { root } = build();
    teardown = initTour(root, gate);
    see(1);
    allowed = false;
    for (const fn of gateSubs) fn(false);
    expect(pause).toHaveBeenCalledTimes(1);
    allowed = true;
    for (const fn of gateSubs) fn(true);
    expect(play).toHaveBeenCalledTimes(2);
  });

  it("pauses when it scrolls below half visible", () => {
    const { root } = build();
    teardown = initTour(root, gate);
    see(1);
    see(0.2);
    expect(pause).toHaveBeenCalledTimes(1);
  });

  it("pauses when the tab is hidden", () => {
    const { root } = build();
    teardown = initTour(root, gate);
    see(1);
    visibility = "hidden";
    document.dispatchEvent(new Event("visibilitychange"));
    expect(pause).toHaveBeenCalledTimes(1);
  });

  it("stays paused when the visitor paused it, through the next intersection", () => {
    const { root, video } = build();
    teardown = initTour(root, gate);
    see(1);
    expect(play).toHaveBeenCalledTimes(1);
    // The visitor presses pause in the controls.
    paused = true;
    video.dispatchEvent(new Event("pause"));
    see(0.1);
    see(1);
    expect(play).toHaveBeenCalledTimes(1);
  });

  it("does not read its own pause as the visitor's", () => {
    const { root, video } = build();
    teardown = initTour(root, gate);
    see(1);
    see(0.1); // initTour pauses
    video.dispatchEvent(new Event("pause"));
    see(1);
    expect(play).toHaveBeenCalledTimes(2);
  });

  it("resumes auto-play after the visitor presses play again", () => {
    const { root, video } = build();
    teardown = initTour(root, gate);
    see(1);
    paused = true;
    video.dispatchEvent(new Event("pause"));
    paused = false;
    video.dispatchEvent(new Event("play"));
    see(0.1);
    see(1);
    expect(play).toHaveBeenCalledTimes(2);
  });

  it("a chapter button seeks and plays, even when not visible and with motion off", () => {
    allowed = false;
    const { root, video } = build();
    teardown = initTour(root, gate);
    root.querySelector<HTMLButtonElement>('button[data-seek="22.2"]')!.click();
    expect(video.currentTime).toBe(22.2);
    expect(play).toHaveBeenCalledTimes(1);
  });

  it("a chapter button plays a video the visitor had paused", () => {
    const { root, video } = build();
    teardown = initTour(root, gate);
    see(1);
    paused = true;
    video.dispatchEvent(new Event("pause"));
    root.querySelector<HTMLButtonElement>('button[data-seek="58.6"]')!.click();
    expect(video.currentTime).toBe(58.6);
    expect(play).toHaveBeenCalledTimes(2);
  });

  it("pauses a video the visitor started from a chapter button when Pause is pressed", () => {
    const { root } = build();
    teardown = initTour(root, gate);
    root.querySelector<HTMLButtonElement>("[data-seek='22.2']")!.click(); // off screen: see() never ran
    expect(play).toHaveBeenCalledTimes(1);
    allowed = false;
    for (const fn of gateSubs) fn(false);
    expect(pause).toHaveBeenCalledTimes(1);
  });

  it("pauses a video started from a chapter button when it leaves the screen", () => {
    const { root } = build();
    teardown = initTour(root, gate);
    see(1);
    see(0.2); // the auto-player pauses
    root.querySelector<HTMLButtonElement>("[data-seek='58.6']")!.click(); // the visitor asks: plays at 20% visible
    expect(paused).toBe(false);
    pause.mockClear();
    see(0); // and scrolls away
    expect(pause).toHaveBeenCalledTimes(1);
  });

  it("enables the chapter buttons at init and disables them again on dispose", () => {
    const { root } = build();
    const buttons = [...root.querySelectorAll<HTMLButtonElement>("[data-seek]")];
    expect(buttons.every((b) => b.disabled)).toBe(true);
    teardown = initTour(root, gate);
    expect(buttons.every((b) => !b.disabled)).toBe(true);
    teardown();
    teardown = null;
    expect(buttons.every((b) => b.disabled)).toBe(true);
  });

  it("writes a chapter start as a valid ISO duration", () => {
    expect([0, 22.2, 58.6, 65, 120].map(isoDuration)).toEqual(["PT0S", "PT22S", "PT58S", "PT1M5S", "PT2M"]);
    expect(clock(22.2)).toBe("0:22");
  });

  it("survives a refused play()", async () => {
    play.mockImplementation(() => Promise.reject(new DOMException("blocked", "NotAllowedError")));
    const { root } = build();
    teardown = initTour(root, gate);
    see(1);
    await Promise.resolve();
    expect(play).toHaveBeenCalled();
  });

  it("marks the block live so the chapters read as buttons, and unmarks it on dispose", () => {
    const { root } = build();
    teardown = initTour(root, gate);
    expect(root.classList.contains("is-live")).toBe(true);
    teardown();
    teardown = null;
    expect(root.classList.contains("is-live")).toBe(false);
  });

  it("removes its listeners on dispose", () => {
    const { root } = build();
    teardown = initTour(root, gate);
    teardown();
    teardown = null;
    root.querySelector<HTMLButtonElement>('button[data-seek="0"]')!.click();
    expect(play).not.toHaveBeenCalled();
    expect(gateSubs).toHaveLength(0);
  });
});
