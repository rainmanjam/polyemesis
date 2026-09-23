// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { Outlet } from "react-router";

/* A TAB LEFT OPEN ACROSS AN UPGRADE.
 *
 * The console's lazy routes (Jobs, Chat, Monitoring, Playout, the clip editor)
 * are separate chunks named by content hash. Upgrade the server and the old
 * hashes are gone: the tab that was already open asks for JobsPage-<old>.js,
 * gets a 404, and the dynamic import rejects. With no error boundary anywhere,
 * React unmounted the whole tree and left `<div id="root"></div>` -- a blank
 * page that Back did not recover, because the import had already failed. */

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      setupStatus: vi.fn().mockResolvedValue({ needsSetup: false, minPasswordChars: 12 }),
      me: vi.fn().mockResolvedValue({ username: "admin" }),
    },
  };
});
vi.mock("@/components/LiveDataProvider", () => ({
  LiveDataProvider: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));
vi.mock("@/components/AppLayout", () => ({
  AppLayout: () => (
    <div>
      <nav>the console chrome</nav>
      <Outlet />
    </div>
  ),
}));
// The chunk the old tab asks for no longer exists on the server.
vi.mock("@/pages/JobsPage", () => {
  throw new TypeError("Failed to fetch dynamically imported module: /assets/JobsPage-3f1c9a.js");
});

import App from "./App";

beforeEach(() => {
  window.history.pushState({}, "", "/jobs");
  vi.spyOn(console, "error").mockImplementation(() => {});
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  window.history.pushState({}, "", "/");
});

describe("App, when a lazy route's chunk will not load", () => {
  it("keeps the console and offers a reload instead of a blank page", async () => {
    const reload = vi.fn();
    vi.stubGlobal("location", { ...window.location, reload, pathname: "/jobs" });

    render(<App />);

    const button = await screen.findByRole("button", { name: "Reload" });
    // The boundary is per route: the chrome around it survived.
    expect(screen.getByText("the console chrome")).toBeTruthy();
    fireEvent.click(button);
    expect(reload).toHaveBeenCalled();
    vi.unstubAllGlobals();
  });
});
