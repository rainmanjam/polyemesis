// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";

class StubIO {
  observe() {}
  disconnect() {}
  unobserve() {}
}

function stubMatchMedia() {
  vi.stubGlobal("IntersectionObserver", StubIO);
  window.matchMedia = ((q: string) => ({
    matches: false,
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.resetModules();
  vi.doUnmock("./motion");
  document.body.innerHTML = "";
});

describe("boot", () => {
  it("boot tolerates a page with no home roots", async () => {
    stubMatchMedia();
    const { boot } = await import("./boot");
    document.body.innerHTML = "<main><p>hello</p></main>";
    let gate: unknown;
    expect(() => {
      gate = boot(document);
    }).not.toThrow();
    expect(typeof (gate as { allowed: unknown }).allowed).toBe("function");
  });

  it("boot wires the pause button to the gate", async () => {
    stubMatchMedia();
    const setUserPaused = vi.fn();
    let paused = false;
    vi.doMock("./motion", async (orig) => {
      const real = await orig<typeof import("./motion")>();
      return {
        ...real,
        createMotionGate: () => ({
          allowed: () => !paused,
          userPaused: () => paused,
          setUserPaused: (p: boolean) => {
            paused = p;
            setUserPaused(p);
          },
          subscribe: () => () => {},
        }),
      };
    });
    const { boot } = await import("./boot");
    document.body.innerHTML = `<button type="button" data-pause aria-pressed="false">Pause motion</button>`;
    const gate = boot(document)!;
    const btn = document.querySelector<HTMLElement>("[data-pause]")!;
    expect(gate.allowed()).toBe(true);
    btn.click();
    expect(gate.allowed()).toBe(false);
    expect(setUserPaused).toHaveBeenCalledTimes(1);
    expect(setUserPaused).toHaveBeenLastCalledWith(true);
    expect(btn.getAttribute("aria-pressed")).toBe("true");
    expect(btn.textContent).toBe("Resume motion");
    btn.click();
    expect(gate.allowed()).toBe(true);
    expect(setUserPaused).toHaveBeenCalledTimes(2);
    expect(setUserPaused).toHaveBeenLastCalledWith(false);
    expect(btn.getAttribute("aria-pressed")).toBe("false");
    expect(btn.textContent).toBe("Pause motion");
  });

  it("with the hero present, one click is still one setUserPaused call", async () => {
    stubMatchMedia();
    const setUserPaused = vi.fn();
    vi.doMock("./motion", async (orig) => {
      const real = await orig<typeof import("./motion")>();
      return {
        ...real,
        createMotionGate: () => ({
          allowed: () => true,
          userPaused: () => false,
          setUserPaused,
          subscribe: () => () => {},
        }),
      };
    });
    const { boot } = await import("./boot");
    document.body.innerHTML = `<section data-hero>
      <div role="tablist" data-story-tabs hidden></div>
      <p data-status></p><p data-fine></p>
      <button type="button" data-pause hidden aria-pressed="false">Pause motion</button></section>`;
    boot(document);
    document.querySelector<HTMLElement>("[data-pause]")!.click();
    expect(setUserPaused).toHaveBeenCalledTimes(1);
  });
});
