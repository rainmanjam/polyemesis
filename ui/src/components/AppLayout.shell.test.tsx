// @vitest-environment jsdom

/* THE SHELL'S KEYBOARD PATH (A79, A80).
 *
 * Two claims a keyboard or screen-reader user depends on. A skip link is the
 * first focusable thing on the page and lands on <main>, so the header and the
 * fifteen nav links are not re-walked on every load. And the mobile nav drawer
 * is removed from the tab order while it is closed: it is only translated off
 * screen, which leaves its links focusable and invisible. */

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { api } from "@/lib/api";
import type { VersionInfo } from "@/lib/types";

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({
    status: null,
    connected: true,
    frameError: false,
    source: null,
    bitrate: [],
    levels: null,
    system: null,
    logs: [],
    programme: null,
    programmeKnown: true,
    snapshotKnown: true,
    sourceCount: 0,
    programmes: [],
    selectProgramme: () => {},
    recordingsRevision: 0,
  }),
  useIngestLive: () => false,
}));

import { AppLayout } from "./AppLayout";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function renderShell() {
  vi.spyOn(api, "version").mockResolvedValue({
    version: "v0.9.0",
    updateAvailable: false,
    comparable: true,
    checkedAt: "2026-09-02T00:00:00Z",
  } as VersionInfo);
  return render(
    <MemoryRouter>
      <AppLayout username="admin" onSignOut={() => {}} />
    </MemoryRouter>,
  );
}

describe("AppLayout: a keyboard user can skip the chrome (A80)", () => {
  it("puts a Skip to content link first, pointing at a focusable main landmark", () => {
    const { container } = renderShell();

    const skip = screen.getByRole("link", { name: "Skip to content" });
    expect(skip.getAttribute("href")).toBe("#main-content");
    // It is the first focusable element in the shell, so one Tab reaches it.
    // Checked against every focusable kind, not only links: an update banner or
    // the tour offer rendered above the link would take the first Tab.
    const firstFocusable = container.querySelector('a[href], button, input, select, textarea, [tabindex]:not([tabindex="-1"])');
    expect(firstFocusable).toBe(skip);

    const main = container.querySelector("main");
    expect(main?.id).toBe("main-content");
    expect(main?.getAttribute("tabindex")).toBe("-1");
  });
});

describe("AppLayout: the closed mobile drawer is out of the tab order (A79)", () => {
  it("hides the nav with visibility while it is closed below md, not only off screen", () => {
    // translate alone leaves the fifteen links focusable and off screen. The
    // visibility class removes them from the tab order once closed.
    renderShell();
    const nav = document.querySelector("nav");
    expect(nav?.className).toContain("max-md:invisible");
    expect(nav?.className).toContain("max-md:-translate-x-full");
  });
});

describe("AppLayout: a phone can see whether it is on air (A78)", () => {
  it("shows the on-air answer beside the menu below sm, in the words the strip uses", () => {
    // Below sm the header had brand, language and sign-out only. The indicator
    // is hidden from sm up, where the full on-air badge already shows.
    renderShell();
    const indicator = screen.getByTestId("mobile-on-air");
    expect(indicator.className).toContain("sm:hidden");
    expect(indicator.textContent).toBe("Off air");
  });
});
