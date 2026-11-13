// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";

const gate = { fake: true };
vi.mock("./home/boot", () => ({ boot: vi.fn(() => gate) }));

import { boot } from "./home/boot";
import { bootInner } from "./inner";

afterEach(() => {
  vi.restoreAllMocks();
  vi.doUnmock("./page-index");
  vi.resetModules();
  document.body.innerHTML = "";
});

describe("bootInner", () => {
  it("runs the shared boot and returns its gate", () => {
    expect(bootInner(document)).toBe(gate);
    expect(boot).toHaveBeenCalledWith(document);
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
});
