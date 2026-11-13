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
  vi.restoreAllMocks();
  vi.resetModules();
  vi.doUnmock("./motion");
  vi.doUnmock("./hero");
  for (const m of ["how-steps", "matrix", "panels", "tour", "deck/index", "calc-mini", "install", "journey"]) vi.doUnmock(`./${m}`);
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

  it("boot shows a Pause button the markup rendered hidden", async () => {
    stubMatchMedia();
    const { boot } = await import("./boot");
    document.body.innerHTML = `<button type="button" data-pause hidden aria-pressed="false">Pause motion</button>`;
    expect(document.querySelector<HTMLElement>("[data-pause]")!.hidden).toBe(true);
    boot(document);
    expect(document.querySelector<HTMLElement>("[data-pause]")!.hidden).toBe(false);
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
      <button type="button" data-pause hidden aria-pressed="false">Pause motion</button></section>`;
    boot(document);
    document.querySelector<HTMLElement>("[data-pause]")!.click();
    expect(setUserPaused).toHaveBeenCalledTimes(1);
    // Let the section's async import settle so it cannot leak into the next test.
    await new Promise((r) => setTimeout(r, 50));
  });
  it("calls the init of each present root, whatever it returns", async () => {
    stubMatchMedia();
    const initHero = vi.fn(() => ({ controller: true }));
    const initHowSteps = vi.fn(() => () => {});
    vi.doMock("./hero", () => ({ initHero }));
    vi.doMock("./how-steps", () => ({ initHowSteps }));
    const { boot } = await import("./boot");
    document.body.innerHTML = `<section data-hero></section><section data-how></section>`;
    boot(document);
    await vi.waitFor(() => {
      expect(initHero).toHaveBeenCalledTimes(1);
      expect(initHowSteps).toHaveBeenCalledTimes(1);
    });
  });

  it("does not request a module whose root is absent", async () => {
    stubMatchMedia();
    const initHero = vi.fn();
    vi.doMock("./hero", () => ({ initHero }));
    const { boot } = await import("./boot");
    document.body.innerHTML = `<p>nothing</p>`;
    boot(document);
    await new Promise((r) => setTimeout(r, 10));
    expect(initHero).not.toHaveBeenCalled();
  });

  it("one broken section does not stop the others or the pause button", async () => {
    stubMatchMedia();
    const errSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    const initHero = vi.fn(() => {
      throw new Error("hero broke");
    });
    const initHowSteps = vi.fn(() => () => {});
    vi.doMock("./hero", () => ({ initHero }));
    vi.doMock("./how-steps", () => ({ initHowSteps }));
    const { boot } = await import("./boot");
    document.body.innerHTML = `<section data-hero></section><section data-how></section>
      <button type="button" data-pause aria-pressed="false">Pause motion</button>`;
    const gate = boot(document)!;
    await vi.waitFor(() => expect(initHowSteps).toHaveBeenCalledTimes(1));
    document.querySelector<HTMLElement>("[data-pause]")!.click();
    expect(gate.userPaused()).toBe(true);
    expect(errSpy).toHaveBeenCalledTimes(1);
    expect(String(errSpy.mock.calls[0][0])).toContain("[data-hero]");
  });

  it("a module whose import rejects does not stop the others", async () => {
    stubMatchMedia();
    const errSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    const initHowSteps = vi.fn(() => () => {});
    vi.doMock("./hero", () => {
      throw new Error("chunk failed");
    });
    vi.doMock("./how-steps", () => ({ initHowSteps }));
    const { boot } = await import("./boot");
    document.body.innerHTML = `<section data-hero></section><section data-how></section>`;
    expect(() => boot(document)).not.toThrow();
    await vi.waitFor(() => expect(initHowSteps).toHaveBeenCalledTimes(1));
    await vi.waitFor(() => expect(errSpy).toHaveBeenCalledTimes(1));
    expect(String(errSpy.mock.calls[0][0])).toContain("[data-hero]");
  });

  it("starts every section of the page from its own root, with the one shared gate", async () => {
    stubMatchMedia();
    const sections = {
      "[data-hero]": ["./hero", "initHero"],
      "[data-how]": ["./how-steps", "initHowSteps"],
      "[data-matrix]": ["./matrix", "initMatrix"],
      "[data-panels]": ["./panels", "initPanels"],
      "[data-tour]": ["./tour", "initTour"],
      "[data-deck]": ["./deck/index", "initDeck"],
      "[data-calc]": ["./calc-mini", "initCalc"],
      "[data-install]": ["./install", "initInstall"],
      "[data-journey]": ["./journey", "initJourney"],
    } as const;
    const inits = new Map<string, ReturnType<typeof vi.fn>>();
    for (const [selector, [path, name]] of Object.entries(sections)) {
      const fn = vi.fn();
      inits.set(selector, fn);
      vi.doMock(path, () => ({ [name]: fn }));
    }
    const { boot } = await import("./boot");
    document.body.innerHTML = Object.keys(sections)
      .map((sel) => `<section ${sel.slice(1, -1)}></section>`)
      .join("");
    const gate = boot(document)!;
    await vi.waitFor(() => {
      for (const fn of inits.values()) expect(fn).toHaveBeenCalledTimes(1);
    });
    for (const [selector, fn] of inits) {
      expect(fn.mock.calls[0][0]).toBe(document.querySelector(selector));
      expect(fn.mock.calls[0][1]).toBe(gate);
    }
  });
});
