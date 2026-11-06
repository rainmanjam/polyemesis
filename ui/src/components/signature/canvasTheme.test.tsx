// @vitest-environment jsdom

import { act, cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { AudioMeter } from "./AudioMeter";
import { Timeline } from "./Timeline";
import { applyTheme } from "@/lib/theme";

/* A canvas cannot follow a CSS variable: it reads the colour once and keeps
 * painting it. These canvases (the meters and the clip timeline) therefore
 * re-read the palette when the theme changes, so flipping Light/Dark repaints
 * them at once -- not at the next reload, and not only when a prop happens to
 * change. jsdom resolves no CSS variables, so getComputedStyle is stubbed with
 * one value per theme. */

const TROUGH = { dark: "rgb(1, 1, 1)", light: "rgb(250, 250, 250)" };

let fills: string[];
let frame: FrameRequestCallback | null;

function fakeContext() {
  const ctx: Record<string, unknown> = {
    fillStyle: "",
    strokeStyle: "",
    globalAlpha: 1,
    lineWidth: 1,
    font: "",
    textAlign: "",
    textBaseline: "",
  };
  for (const m of ["setTransform", "clearRect", "save", "restore", "beginPath", "rect", "clip", "moveTo", "lineTo", "stroke", "fill", "arc", "closePath", "setLineDash", "fillText", "strokeRect", "translate", "scale"]) {
    ctx[m] = vi.fn();
  }
  ctx.measureText = vi.fn(() => ({ width: 10 }));
  ctx.createLinearGradient = vi.fn(() => ({ addColorStop: vi.fn() }));
  ctx.fillRect = vi.fn(() => {
    fills.push(String(ctx.fillStyle));
  });
  return ctx;
}

beforeEach(() => {
  fills = [];
  frame = null;
  document.documentElement.removeAttribute("data-theme");
  vi.spyOn(window, "getComputedStyle").mockImplementation(
    () =>
      ({
        getPropertyValue: (name: string) =>
          name === "--meter-bg" ? TROUGH[document.documentElement.dataset.theme === "light" ? "light" : "dark"] : "",
      }) as unknown as CSSStyleDeclaration,
  );
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
    frame = cb;
    return 1;
  });
  vi.stubGlobal("cancelAnimationFrame", () => {
    frame = null;
  });
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      disconnect() {}
    },
  );
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(400);
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => fakeContext() as unknown as CanvasRenderingContext2D);
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("AudioMeter", () => {
  it("repaints its trough in the new theme's colour when the theme flips, with no remount", () => {
    render(<AudioMeter rms={[-20]} peak={[-20]} />);
    frame?.(1016);
    expect(fills).toContain(TROUGH.dark);
    fills.length = 0;

    act(() => applyTheme("light"));
    // The observer fires as a microtask; the next frame is the first to see it.
    return Promise.resolve().then(() => {
      frame?.(1032);
      expect(fills).toContain(TROUGH.light);
      expect(fills).not.toContain(TROUGH.dark);
    });
  });

  it("stops watching the theme when it unmounts", () => {
    const { unmount } = render(<AudioMeter rms={[-20]} peak={[-20]} />);
    unmount();
    expect(() => applyTheme("light")).not.toThrow();
  });
});

describe("Timeline", () => {
  const props = {
    variant: "detail" as const,
    viewStartMs: 0,
    viewEndMs: 10000,
    inMs: 1000,
    outMs: 9000,
    playheadMs: 2000,
    keyframes: [],
  };

  it("repaints in the new theme's colours when the theme flips, with no prop change", async () => {
    render(<Timeline {...props} />);
    expect(fills).toContain(TROUGH.dark);
    fills.length = 0;

    await act(async () => {
      applyTheme("light");
      await Promise.resolve();
    });
    expect(fills).toContain(TROUGH.light);
    expect(fills).not.toContain(TROUGH.dark);
  });
});
