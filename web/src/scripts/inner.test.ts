// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";

const gate = { fake: true };
vi.mock("./home/boot", () => ({ boot: vi.fn(() => gate) }));

import { boot } from "./home/boot";
import { bootInner } from "./inner";

afterEach(() => {
  vi.mocked(boot).mockClear();
  vi.restoreAllMocks();
  vi.doUnmock("./page-index");
  vi.doUnmock("./chooser");
  vi.resetModules();
  document.body.innerHTML = "";
});

describe("bootInner", () => {
  it("runs the shared boot and returns its gate", () => {
    expect(bootInner(document)).toBe(gate);
    expect(boot).toHaveBeenCalledWith(document);
  });

  it("waits for DOMContentLoaded while the document is still loading", () => {
    const state = vi.spyOn(document, "readyState", "get").mockReturnValue("loading");
    document.body.innerHTML = `<fieldset data-js-only hidden></fieldset>`;
    expect(bootInner(document)).toBeUndefined();
    expect(boot).not.toHaveBeenCalled();
    expect(document.querySelector<HTMLElement>("[data-js-only]")!.hidden).toBe(true);
    state.mockReturnValue("interactive");
    document.dispatchEvent(new Event("DOMContentLoaded"));
    expect(boot).toHaveBeenCalledTimes(1);
    expect(document.querySelector<HTMLElement>("[data-js-only]")!.hidden).toBe(false);
  });

  it("un-hides data-js-only elements and leaves the others hidden", () => {
    document.body.innerHTML = `<fieldset data-js-only hidden></fieldset><div id="x" hidden></div>`;
    bootInner(document);
    expect(document.querySelector<HTMLElement>("[data-js-only]")!.hidden).toBe(false);
    expect(document.getElementById("x")!.hidden).toBe(true);
  });

  it("does nothing, and throws nothing, on a page with none of the roots", () => {
    document.body.innerHTML = "<p>plain</p>";
    expect(() => bootInner(document)).not.toThrow();
  });

  it("starts the index on each [data-index] root", async () => {
    document.body.innerHTML = `<div data-index><a href="#s">s</a></div><section id="s"></section>`;
    bootInner(document);
    await vi.waitFor(() => expect(document.querySelector("a")!.getAttribute("aria-current")).toBe("true"));
  });

  it("logs, and does not throw, when the index fails to start", async () => {
    vi.doMock("./page-index", () => ({
      initPageIndex: () => {
        throw new Error("boom");
      },
    }));
    const err = vi.spyOn(console, "error").mockImplementation(() => {});
    document.body.innerHTML = `<div data-index></div>`;
    bootInner(document);
    await vi.waitFor(() => expect(err).toHaveBeenCalled());
  });

  it("starts the chooser when a [data-chooser] root exists", async () => {
    const initChooser = vi.fn();
    vi.doMock("./chooser", () => ({ initChooser }));
    document.body.innerHTML = `<fieldset data-chooser hidden></fieldset>`;
    bootInner(document);
    await vi.waitFor(() => expect(initChooser).toHaveBeenCalledWith(document.querySelector("[data-chooser]")));
  });

  it("does not import the chooser module on a page without the root", async () => {
    const factory = vi.fn(() => ({ initChooser: vi.fn() }));
    vi.doMock("./chooser", factory);
    document.body.innerHTML = "<p>plain</p>";
    bootInner(document);
    await new Promise((r) => setTimeout(r, 20));
    expect(factory).not.toHaveBeenCalled();
  });

  it("logs, and does not throw, when the chooser fails to start", async () => {
    vi.doMock("./chooser", () => ({
      initChooser: () => {
        throw new Error("boom");
      },
    }));
    const err = vi.spyOn(console, "error").mockImplementation(() => {});
    document.body.innerHTML = `<fieldset data-chooser></fieldset>`;
    bootInner(document);
    await vi.waitFor(() => expect(err).toHaveBeenCalled());
  });
});

describe("bootInner on a page opened at a #fragment", () => {
  const withTarget = () => {
    document.body.innerHTML = `<section id="need"></section>`;
    const target = document.getElementById("need")!;
    const scroll = vi.fn();
    target.scrollIntoView = scroll;
    return scroll;
  };

  afterEach(() => {
    window.location.hash = "";
  });

  it("puts the section back where the link meant, at once", () => {
    const scroll = withTarget();
    window.location.hash = "#need";
    bootInner(document);
    expect(scroll).toHaveBeenCalledWith({ block: "start", behavior: "instant" });
  });

  it("does nothing without a fragment", () => {
    const scroll = withTarget();
    bootInner(document);
    expect(scroll).not.toHaveBeenCalled();
  });

  it("ignores a fragment that names nothing or cannot be decoded", () => {
    const scroll = withTarget();
    window.location.hash = "#nowhere";
    expect(() => bootInner(document)).not.toThrow();
    window.location.hash = "#%";
    expect(() => bootInner(document)).not.toThrow();
    expect(scroll).not.toHaveBeenCalled();
  });

  it("leaves the position alone on a reload or a Back", () => {
    const scroll = withTarget();
    window.location.hash = "#need";
    vi.spyOn(performance, "getEntriesByType").mockReturnValue([{ type: "reload" }] as unknown as PerformanceEntryList);
    bootInner(document);
    expect(scroll).not.toHaveBeenCalled();
  });

  it("realigns on a fresh navigation", () => {
    const scroll = withTarget();
    window.location.hash = "#need";
    vi.spyOn(performance, "getEntriesByType").mockReturnValue([{ type: "navigate" }] as unknown as PerformanceEntryList);
    bootInner(document);
    expect(scroll).toHaveBeenCalledTimes(1);
  });
});

describe("bootInner holds a #fragment while the page settles", () => {
  let resized: (() => void) | undefined;
  const disconnect = vi.fn();
  const observe = vi.fn();

  const setUp = () => {
    document.body.innerHTML = `<section id="need"></section>`;
    const scroll = vi.fn();
    document.getElementById("need")!.scrollIntoView = scroll;
    window.location.hash = "#need";
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(cb: () => void) {
          resized = cb;
        }
        observe = observe;
        disconnect = disconnect;
      },
    );
    return scroll;
  };

  afterEach(() => {
    window.location.hash = "";
    resized = undefined;
    disconnect.mockClear();
    observe.mockClear();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("realigns again when the page changes height", () => {
    const scroll = setUp();
    bootInner(document);
    expect(observe).toHaveBeenCalledWith(document.body);
    resized?.();
    expect(scroll).toHaveBeenCalledTimes(2);
    window.dispatchEvent(new Event("keydown")); // let go, so no listener outlives the test
  });

  it("lets go as soon as the visitor scrolls, taps or types", () => {
    setUp();
    bootInner(document);
    window.dispatchEvent(new Event("wheel"));
    expect(disconnect).toHaveBeenCalledTimes(1);
  });

  it("lets go by itself after two seconds", () => {
    vi.useFakeTimers();
    setUp();
    bootInner(document);
    vi.advanceTimersByTime(1999);
    expect(disconnect).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(disconnect).toHaveBeenCalledTimes(1);
  });
});
