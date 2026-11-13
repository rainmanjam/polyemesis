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
  vi.doUnmock("./home/failover");
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

  it("starts every chooser on the page, not only the first", async () => {
    const initChooser = vi.fn();
    vi.doMock("./chooser", () => ({ initChooser }));
    document.body.innerHTML = `<fieldset data-chooser id="c1" hidden></fieldset><fieldset data-chooser id="c2" hidden></fieldset>`;
    bootInner(document);
    await vi.waitFor(() => expect(initChooser).toHaveBeenCalledTimes(2));
    expect(initChooser).toHaveBeenCalledWith(document.getElementById("c2"));
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

  it("starts each failover root when a gate exists", async () => {
    const initFailover = vi.fn();
    vi.doMock("./home/failover", () => ({ initFailover }));
    document.body.innerHTML = `<div data-failover id="a"></div><div data-failover id="b"></div>`;
    bootInner(document);
    await vi.waitFor(() => expect(initFailover).toHaveBeenCalledTimes(2));
    expect(initFailover).toHaveBeenCalledWith(document.getElementById("a"), gate);
  });

  it("does not import the failover module on a page without the root", async () => {
    const factory = vi.fn(() => ({ initFailover: vi.fn() }));
    vi.doMock("./home/failover", factory);
    document.body.innerHTML = "<p>plain</p>";
    bootInner(document);
    await new Promise((r) => setTimeout(r, 20));
    expect(factory).not.toHaveBeenCalled();
  });

  it("does not start the failover demo when there is no gate", async () => {
    const initFailover = vi.fn();
    vi.doMock("./home/failover", () => ({ initFailover }));
    vi.mocked(boot).mockReturnValueOnce(undefined as never);
    document.body.innerHTML = `<div data-failover></div>`;
    bootInner(document);
    await new Promise((r) => setTimeout(r, 20));
    expect(initFailover).not.toHaveBeenCalled();
  });

  it("logs, and does not throw, when the failover demo fails to start", async () => {
    vi.doMock("./home/failover", () => ({
      initFailover: () => {
        throw new Error("boom");
      },
    }));
    const err = vi.spyOn(console, "error").mockImplementation(() => {});
    document.body.innerHTML = `<div data-failover></div>`;
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

  it("does nothing on a back_forward navigation either", () => {
    const scroll = withTarget();
    window.location.hash = "#need";
    vi.spyOn(performance, "getEntriesByType").mockReturnValue([
      { type: "back_forward" },
    ] as unknown as PerformanceEntryList);
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
    Object.defineProperty(window, "scrollY", { value: 0, configurable: true });
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

  it.each(["wheel", "touchstart", "pointerdown", "keydown"])("lets go on a %s", (type) => {
    setUp();
    bootInner(document);
    window.dispatchEvent(new Event(type));
    expect(disconnect).toHaveBeenCalledTimes(1);
  });

  it("does not realign on a resize after the visitor has let go", () => {
    const scroll = setUp();
    bootInner(document);
    window.dispatchEvent(new Event("pointerdown"));
    scroll.mockClear();
    resized?.(); // the callback of an observer that has been disconnected must still not pull the page
    expect(scroll).not.toHaveBeenCalled();
  });

  /* A scroll the page's own align() causes must not count as the visitor's. */
  const scrollTo = (y: number) => {
    Object.defineProperty(window, "scrollY", { value: y, configurable: true });
    window.dispatchEvent(new Event("scroll"));
  };

  it("lets go when something else moves the page (a scrollbar drag, find-in-page, assistive technology)", () => {
    const scroll = setUp();
    scroll.mockImplementation(() => Object.defineProperty(window, "scrollY", { value: 300, configurable: true }));
    bootInner(document);
    scrollTo(900);
    expect(disconnect).toHaveBeenCalledTimes(1);
    scroll.mockClear();
    resized?.();
    expect(scroll).not.toHaveBeenCalled();
  });

  it("keeps holding through the scroll its own align() causes, to the pixel and one pixel over", () => {
    const scroll = setUp();
    scroll.mockImplementation(() => Object.defineProperty(window, "scrollY", { value: 300, configurable: true }));
    bootInner(document);
    scrollTo(300);
    scrollTo(301);
    scrollTo(299);
    expect(disconnect).not.toHaveBeenCalled();
    resized?.();
    expect(scroll).toHaveBeenCalledTimes(2);
    window.dispatchEvent(new Event("keydown"));
  });

  it("lets go by itself after one second", () => {
    vi.useFakeTimers();
    setUp();
    bootInner(document);
    vi.advanceTimersByTime(999);
    expect(disconnect).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(disconnect).toHaveBeenCalledTimes(1);
  });
});

describe("bootInner opens the tab a #fragment lives in", () => {
  /* A fake tablist: clicking a tab shows its panel and hides the others, as tabs.ts does. */
  const tabs = () => {
    document.body.innerHTML = `
      <div role="tablist"><button role="tab" id="tab-docker" aria-controls="panel-docker">Docker</button>
        <button role="tab" id="tab-compose" aria-controls="panel-compose">Compose</button></div>
      <div role="tabpanel" id="panel-docker"><p id="inner-docker">d</p></div>
      <div role="tabpanel" id="panel-compose" hidden><p id="inner-compose">c</p></div>`;
    for (const tab of document.querySelectorAll<HTMLElement>("[role=tab]")) {
      tab.addEventListener("click", () => {
        for (const panel of document.querySelectorAll<HTMLElement>("[role=tabpanel]")) {
          panel.hidden = panel.id !== tab.getAttribute("aria-controls");
        }
      });
    }
    const scrolls: boolean[] = []; // whether the target was visible at each align
    for (const id of ["panel-compose", "inner-compose", "inner-docker"]) {
      document.getElementById(id)!.scrollIntoView = () => {
        scrolls.push(!document.getElementById(id)!.closest("[hidden]"));
      };
    }
    return scrolls;
  };
  let resized: (() => void) | undefined;

  const hold = () => {
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(cb: () => void) {
          resized = cb;
        }
        observe() {}
        disconnect() {}
      },
    );
  };

  afterEach(() => {
    window.location.hash = "";
    resized = undefined;
    vi.unstubAllGlobals();
    window.dispatchEvent(new Event("keydown")); // let go of any hold the test left
  });

  it("clicks the tab that controls a hidden panel the fragment names, then aligns on the visible target", () => {
    const scrolls = tabs();
    window.location.hash = "#panel-compose";
    bootInner(document);
    expect(document.getElementById("panel-compose")!.hidden).toBe(false);
    expect(document.getElementById("panel-docker")!.hidden).toBe(true);
    expect(scrolls.at(-1)).toBe(true);
  });

  it("does the same for a target inside a hidden panel", () => {
    const scrolls = tabs();
    window.location.hash = "#inner-compose";
    bootInner(document);
    expect(document.getElementById("panel-compose")!.hidden).toBe(false);
    expect(scrolls.at(-1)).toBe(true);
  });

  it("opens the panel again when the tabs module hides it after the first align", () => {
    const scrolls = tabs();
    hold();
    window.location.hash = "#panel-compose";
    bootInner(document);
    // initTabs arrives later and hides everything but the first panel:
    document.getElementById("panel-compose")!.hidden = true;
    document.getElementById("panel-docker")!.hidden = false;
    resized?.();
    expect(document.getElementById("panel-compose")!.hidden).toBe(false);
    expect(scrolls.at(-1)).toBe(true);
  });

  it("leaves tabs alone when the target is in a visible panel or has no panel", () => {
    const scrolls = tabs();
    window.location.hash = "#inner-docker";
    bootInner(document);
    expect(document.getElementById("panel-compose")!.hidden).toBe(true);
    expect(scrolls).toEqual([true]);
  });

  it("does not throw when the hidden panel has no tab", () => {
    const scrolls = tabs();
    document.getElementById("tab-compose")!.remove();
    window.location.hash = "#panel-compose";
    expect(() => bootInner(document)).not.toThrow();
    expect(scrolls).toHaveLength(1);
  });
});
