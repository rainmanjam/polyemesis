// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

/* WHAT IS ACTUALLY WORTH TESTING HERE IS NOT "DOES IT DRAW A DIAGRAM".
 *
 * Mermaid draws the diagram; that it does so was verified in a real browser,
 * and re-asserting it against a mock would only prove the mock was called.
 *
 * The two behaviours that are this module's own, and that fail silently, are:
 *
 *   1. It must NOT import mermaid on a page with no diagram. That guard is the
 *      only reason a parser, a layout engine and a renderer are not in the
 *      bundle of all 23 documentation pages. Delete it and nothing breaks,
 *      nothing goes red, and every page gets slower -- the exact shape of a
 *      regression nobody notices until someone profiles the site.
 *
 *   2. A diagram that fails to render must LEAVE ITS SOURCE ON THE PAGE. A
 *      blank space is indistinguishable from a page that forgot to include the
 *      diagram, and mermaid throws on a syntax error in a way that is otherwise
 *      invisible outside devtools -- nothing in the build parses these fences.
 *
 * The module runs its work at import time, so each test sets the DOM up first
 * and then imports it through a reset module registry. */

const renderMock = vi.fn();
const initializeMock = vi.fn();

vi.mock("mermaid", () => ({
  default: { initialize: initializeMock, render: renderMock },
}));

/** Puts n diagram containers on the page, as hast-mermaid.mjs emits them. */
function givenDiagrams(n: number, source = "flowchart TD\n  a --> b") {
  document.body.innerHTML = Array.from(
    { length: n },
    () => `<div class="mermaid" data-mermaid="pending"></div>`,
  ).join("");
  document.querySelectorAll("[data-mermaid]").forEach((el) => {
    el.textContent = source;
  });
}

/** Imports the module fresh and lets its top-level work settle. */
async function run() {
  vi.resetModules();
  await import("./mermaid-render.ts");
  // The module's work is async; a macrotask turn is enough for the awaited
  // dynamic import and the render loop to finish under a mocked mermaid.
  await new Promise((r) => setTimeout(r, 0));
}

beforeEach(() => {
  renderMock.mockReset();
  initializeMock.mockReset();
  renderMock.mockResolvedValue({ svg: "<svg><g>drawn</g></svg>" });
  document.body.innerHTML = "";
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("mermaid-render", () => {
  it("does not touch mermaid at all on a page with no diagram", async () => {
    // 22 of the 23 documents. This is the assertion that keeps the library out
    // of their bundles.
    await run();

    expect(initializeMock).not.toHaveBeenCalled();
    expect(renderMock).not.toHaveBeenCalled();
  });

  it("renders every diagram on the page and marks each one done", async () => {
    givenDiagrams(2);

    await run();

    expect(renderMock).toHaveBeenCalledTimes(2);
    const states = [...document.querySelectorAll("[data-mermaid]")].map(
      (el) => (el as HTMLElement).dataset.mermaid,
    );
    expect(states).toEqual(["done", "done"]);
  });

  it("hands mermaid the fence source, not the element's markup", async () => {
    givenDiagrams(1, "flowchart TD\n  a -- no --> b");

    await run();

    const [, source] = renderMock.mock.calls[0];
    expect(source).toContain("-->");
    expect(source).not.toContain("<div");
  });

  it("gives each diagram a distinct id, so two on a page cannot collide", async () => {
    givenDiagrams(3);

    await run();

    const ids = renderMock.mock.calls.map(([id]) => id);
    expect(new Set(ids).size).toBe(3);
  });

  it("LEAVES THE SOURCE VISIBLE when a diagram fails to render", async () => {
    const source = "flowchart TD\n  this is not valid mermaid";
    givenDiagrams(1, source);
    renderMock.mockRejectedValue(new Error("Parse error"));
    // The module logs the failure; keep it out of the test output while still
    // asserting it happened, because a silent failure here is the thing being
    // guarded against.
    const err = vi.spyOn(console, "error").mockImplementation(() => {});

    await run();

    const el = document.querySelector("[data-mermaid]") as HTMLElement;
    expect(el.dataset.mermaid).toBe("failed");
    // The source survived: a reader sees the flowchart's text rather than a
    // gap where a diagram should be.
    expect(el.textContent).toContain("flowchart TD");
    expect(el.querySelector("svg")).toBeNull();
    expect(err).toHaveBeenCalled();
  });

  it("keeps rendering the rest of the page after one diagram fails", async () => {
    givenDiagrams(2);
    renderMock
      .mockRejectedValueOnce(new Error("Parse error"))
      .mockResolvedValue({ svg: "<svg><g>drawn</g></svg>" });
    vi.spyOn(console, "error").mockImplementation(() => {});

    await run();

    const states = [...document.querySelectorAll("[data-mermaid]")].map(
      (el) => (el as HTMLElement).dataset.mermaid,
    );
    expect(states).toEqual(["failed", "done"]);
  });

  it("pins securityLevel, because innerHTML downstream depends on it", async () => {
    // The renderer assigns mermaid's output with innerHTML. That is safe
    // BECAUSE mermaid sanitises under securityLevel "strict"; loosening it
    // silently changes what that assignment means.
    givenDiagrams(1);

    await run();

    expect(initializeMock).toHaveBeenCalledTimes(1);
    expect(initializeMock.mock.calls[0][0]).toMatchObject({
      securityLevel: "strict",
      startOnLoad: false,
    });
  });
});
