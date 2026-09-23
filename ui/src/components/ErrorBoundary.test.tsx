// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { AppBoundary } from "./ErrorBoundary";

/* THE OUTERMOST BOUNDARY.
 *
 * LazyBoundary catches a route that fails; App.chunkError.test.tsx covers it.
 * AppBoundary is the last word for an error OUTSIDE any route -- AppLayout,
 * LiveDataProvider, the login gate -- and without it such an error unmounts
 * the whole tree to an empty #root, the blank page exploratory #29 reported.
 * Two halves: the boundary renders its notice, and main.tsx actually mounts
 * the app inside it (removing the wrapper is the regression that passes every
 * behavioural test, because none of them render main.tsx). */

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function Explodes(): never {
  throw new Error("AppLayout blew up");
}

describe("AppBoundary", () => {
  it("replaces a crashed tree with a full-screen notice and a Reload button", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const reload = vi.fn();
    vi.stubGlobal("location", { ...window.location, reload });

    render(
      <AppBoundary>
        <Explodes />
      </AppBoundary>,
    );

    const notice = screen.getByRole("alert");
    expect(notice.textContent).toContain("This page failed to load.");
    // Full-screen: nothing else survived to frame it.
    expect(notice.className).toContain("h-dvh");
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    expect(reload).toHaveBeenCalled();
  });

  it("renders its children untouched when nothing throws", () => {
    render(
      <AppBoundary>
        <p>the console</p>
      </AppBoundary>,
    );
    expect(screen.getByText("the console")).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("is what main.tsx mounts the app inside", () => {
    // __dirname, not import.meta.url: under jsdom the module URL is not file:.
    const main = readFileSync(join(__dirname, "..", "main.tsx"), "utf8");
    // <App /> directly inside <AppBoundary>, whitespace aside.
    expect(main).toMatch(/<AppBoundary>\s*<App\s*\/>\s*<\/AppBoundary>/);
  });
});
