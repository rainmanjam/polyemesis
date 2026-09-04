// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render } from "@testing-library/react";

import { AudioMeter } from "./AudioMeter";

/* The meter's redundant encoding lives entirely inside a canvas draw loop, and
 * in jsdom `getContext("2d")` returns null, so the effect returns on its third
 * line and NOTHING below it has ever run under test. That is the whole reason
 * this file exists: the accessibility argument the component was built to make
 * — that level is readable with the colour deleted — is made by code no test
 * has ever executed.
 *
 * So the context is faked and the frame loop is driven by hand. What is
 * asserted is not "these draw calls happen in this order"; it is the three
 * non-colour channels the component claims:
 *
 *   TEXTURE  the hatch appears only once the bar is actually hot
 *   POSITION the zone marks are firm over the lit bar and faint over the trough
 *   TEXT     the CLIP flag raises on a clip and, crucially, clears again
 *
 * plus the peak hold falling back, which is what stops the meter turning into a
 * session high-water mark within a minute.
 */

/** dbToFraction maps -60..0 onto 0..1, and clientWidth is 0 in jsdom, so the
 *  component falls back to 200 CSS px. Both are needed to say where a mark
 *  should land, so they are named once here rather than as bare numbers below. */
const WIDTH = 200;
const x = (db: number) => Math.round(((db + 60) / 60) * WIDTH);

/** The palette's fallbacks, used because jsdom resolves every CSS variable to
 *  "". They identify which channel drew a rectangle. */
const HOLD = "#e6e9ef";
const PEAK = "#e5484d";

interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
  fill: string;
  alpha: number;
}

let rects: Rect[];
let strokes: number;
let frame: FrameRequestCallback | null;
let clock: number;

function fakeContext() {
  const ctx = {
    fillStyle: "",
    strokeStyle: "",
    globalAlpha: 1,
    lineWidth: 1,
    setTransform: vi.fn(),
    clearRect: vi.fn(),
    save: vi.fn(),
    restore: vi.fn(),
    beginPath: vi.fn(),
    rect: vi.fn(),
    clip: vi.fn(),
    moveTo: vi.fn(),
    lineTo: vi.fn(),
    stroke: vi.fn(() => {
      strokes++;
    }),
    createLinearGradient: vi.fn(() => ({ addColorStop: vi.fn() })),
    fillRect: vi.fn((rx: number, ry: number, rw: number, rh: number) => {
      rects.push({ x: rx, y: ry, w: rw, h: rh, fill: ctx.fillStyle, alpha: ctx.globalAlpha });
    }),
  };
  return ctx;
}

/** Advance the clock and run exactly one frame. The loop re-registers itself,
 *  so each call runs one draw and leaves the next one pending. */
function tick(ms: number) {
  clock += ms;
  const f = frame;
  frame = null;
  f?.(clock);
}

beforeEach(() => {
  rects = [];
  strokes = 0;
  frame = null;
  clock = 1000;
  vi.spyOn(performance, "now").mockImplementation(() => clock);
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
    frame = cb;
    return 1;
  });
  vi.stubGlobal("cancelAnimationFrame", () => {
    frame = null;
  });
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(
    () => fakeContext() as unknown as CanvasRenderingContext2D,
  );
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("texture: the hot end of the bar is a different surface, not just a different hue", () => {
  it("does not hatch a bar that has not reached -6 dBFS", () => {
    render(<AudioMeter rms={[-12]} peak={[-12]} />);
    tick(16);
    // hatchHot is the only thing in the whole loop that strokes.
    expect(strokes).toBe(0);
  });

  it("hatches a bar that has, and only from -6 rightwards", () => {
    render(<AudioMeter rms={[-3]} peak={[-3]} />);
    tick(16);
    expect(strokes).toBe(1);
  });
});

describe("position: an operator can read level off geometry", () => {
  it("marks the zone breaks firmly over the lit bar and faintly over the trough", () => {
    // rms -12 lights the bar as far as the -12 break, so the -18 mark is under
    // signal and the -2 mark is over empty trough. If both were drawn at the
    // same weight the marks would be four permanent lines across a meter that
    // is usually quiet — decoration an eye learns to filter out, and it would
    // be filtering out the thing they are here to say.
    render(<AudioMeter rms={[-12]} peak={[-12]} />);
    tick(16);

    const marks = rects.filter((r) => r.fill === HOLD && r.w === 1);
    const lit = marks.find((r) => r.x === x(-18));
    const unlit = marks.find((r) => r.x === x(-2));

    expect(lit, "no zone-break mark drawn at -18 dBFS").toBeDefined();
    expect(unlit, "no zone-break mark drawn at -2 dBFS").toBeDefined();
    expect(lit!.alpha).toBeGreaterThan(unlit!.alpha);
  });

  it("draws all four breaks, so the two the scale cannot label are still marked", () => {
    render(<AudioMeter rms={[-30]} peak={[-30]} />);
    tick(16);
    const marks = rects.filter((r) => r.fill === HOLD && r.w === 1);
    expect(marks.map((r) => r.x).sort((a, b) => a - b)).toEqual(
      [x(-18), x(-12), x(-6), x(-2)].sort((a, b) => a - b),
    );
  });
});

describe("the peak hold falls back instead of becoming a high-water mark", () => {
  it("holds, then decays once the hold time has passed", () => {
    const { rerender } = render(<AudioMeter rms={[-30]} peak={[-6]} />);
    tick(16);

    const held = rects.filter((r) => r.w === 2).pop();
    expect(held, "no peak-hold tick drawn").toBeDefined();
    expect(held!.x).toBe(x(-6));

    // Signal drops away. Within the hold window the tick must not move.
    rerender(<AudioMeter rms={[-60]} peak={[-60]} />);
    rects.length = 0;
    tick(500);
    expect(rects.filter((r) => r.w === 2).pop()!.x).toBe(x(-6));

    // Past it, the tick falls at 24 dB/s.
    rects.length = 0;
    tick(1200);
    const fallen = rects.filter((r) => r.w === 2).pop();
    expect(fallen!.x).toBeLessThan(x(-6));
  });
});

describe("text: the channel that survives the meter being too small to read", () => {
  it("raises the CLIP flag on a clip and lowers it again once the latch expires", () => {
    const { container, rerender } = render(<AudioMeter rms={[-30]} peak={[-30]} />);
    const flag = container.querySelector("span[aria-hidden]") as HTMLSpanElement;
    expect(flag.textContent).toBe("CLIP");

    tick(16);
    expect(flag.style.visibility).toBe("hidden");

    rerender(<AudioMeter rms={[-1]} peak={[-0.1]} />);
    tick(16);
    expect(
      flag.style.visibility,
      "peak reached -0.1 dBFS, past the -0.2 clip level, and nothing said so in text",
    ).toBe("visible");

    // A clip you did not see is a clip you cannot fix, so the latch lingers.
    rerender(<AudioMeter rms={[-30]} peak={[-30]} />);
    rects.length = 0;
    tick(1000);
    expect(flag.style.visibility).toBe("visible");
    expect(rects.some((r) => r.fill === PEAK && r.x === WIDTH - 3)).toBe(true);

    // But it does clear, or the meter reports a clip from a minute ago forever.
    tick(1000);
    expect(flag.style.visibility).toBe("hidden");
  });
});

describe("shape", () => {
  it("draws one row per channel", () => {
    render(<AudioMeter rms={[-6, -12, -30]} peak={[-6, -12, -30]} barHeight={10} barGap={3} />);
    tick(16);
    const troughs = rects.filter((r) => r.w === WIDTH);
    expect(troughs.map((r) => r.y)).toEqual([0, 13, 26]);
  });

  it("reads a missing peak as silence, never as full scale", () => {
    // rms and peak are two independent arrays, so a caller can hand over a
    // short peak — a channel added upstream before the peak analyser knows
    // about it. The absent reading has to floor at MIN_DB: defaulting the
    // other way puts the sample at 0 dBFS, which is past the clip level, so
    // three silent channels would raise a CLIP flag and hold it.
    const { container } = render(<AudioMeter rms={[-30, -30, -30]} peak={[-30]} />);
    tick(16);
    const flag = container.querySelector("span[aria-hidden]") as HTMLSpanElement;
    expect(flag.style.visibility).toBe("hidden");
    // The one channel that does have a reading still meters; the two without
    // one draw no hold tick at all, rather than a tick pinned at full scale.
    const ticks = rects.filter((r) => r.w === 2 || r.w === 3);
    expect(ticks.map((r) => r.y)).toEqual([0]);
  });
});
