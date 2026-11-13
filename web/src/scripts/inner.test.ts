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
