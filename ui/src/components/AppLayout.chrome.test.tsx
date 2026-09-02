// @vitest-environment jsdom
//
// THE CHROME'S VERSION WIRING, WHICH HAD NO TEST AND CARRIED TWO CLAIMS.
//
// #660 put the running version in the chrome. The claim that matters is not
// that it renders -- VersionTag.test.tsx pins that -- but that the chrome gets
// it WITHOUT a second /version call. That endpoint surveys what a restart
// would interrupt fresh on every request, so a second caller means a second
// on-air survey on every page load. UpdateBanner already holds the answer and
// hands it over through onInfo; nothing was checking that it still does.
//
// A property asserted only in a commit message is not asserted.

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
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
    recordingsRevision: 0,
  }),
  useIngestLive: () => false,
}));

import { AppLayout } from "./AppLayout";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const info = {
  version: "v0.9.0",
  updateAvailable: false,
  comparable: true,
  checkedAt: "2026-09-02T00:00:00Z",
} as VersionInfo;

function renderChrome() {
  return render(
    <MemoryRouter>
      <AppLayout username="admin" onSignOut={() => {}} />
    </MemoryRouter>,
  );
}

describe("the chrome's version wiring", () => {
  it("shows the running version on an install with no update available", async () => {
    // The state in which UpdateBanner renders nothing at all, which is why the
    // version used to be invisible everywhere. #660.
    const version = vi.spyOn(api, "version").mockResolvedValue(info);
    renderChrome();
    await waitFor(() => expect(screen.getByText("v0.9.0")).toBeTruthy());
    expect(version).toHaveBeenCalled();
  });

  it("asks the server for it exactly once", async () => {
    // The property the whole arrangement exists for. Giving VersionTag its own
    // fetch would be simpler and would double the on-air survey on every page
    // load; this is what would catch that being done.
    const version = vi.spyOn(api, "version").mockResolvedValue(info);
    renderChrome();
    await waitFor(() => expect(screen.getByText("v0.9.0")).toBeTruthy());
    expect(
      version.mock.calls.length,
      "/version was requested more than once for a single page load, and each call re-surveys what a restart would interrupt",
    ).toBe(1);
  });

  it("checks for an update once when the server has never checked, and shows the refreshed version", async () => {
    // UpdateBanner's rule: the CACHED answer on mount, never a check -- except
    // once, when checkedAt is empty, because an install nobody clicks would
    // otherwise never learn anything. The refreshed answer must reach the
    // chrome too, or the tag would sit on the stale one for the session.
    const version = vi
      .spyOn(api, "version")
      .mockResolvedValue({ ...info, version: "v0.9.0", checkedAt: undefined } as VersionInfo);
    const check = vi
      .spyOn(api, "checkUpdate")
      .mockResolvedValue({ ...info, version: "v0.9.1", checkedAt: "now" } as VersionInfo);

    renderChrome();

    await waitFor(() => expect(screen.getByText("v0.9.1")).toBeTruthy());
    expect(version).toHaveBeenCalledTimes(1);
    expect(
      check.mock.calls.length,
      "a check ran more than once for a single page load, which is what turns a pull-only design into a phone-home one",
    ).toBe(1);
  });

  it("does not check when the server already has", async () => {
    // The control. Checking on every load reaches GitHub from every operator's
    // browser session, which is the property this feature was built to avoid.
    vi.spyOn(api, "version").mockResolvedValue(info); // checkedAt present
    const check = vi.spyOn(api, "checkUpdate").mockResolvedValue(info);
    renderChrome();
    await waitFor(() => expect(screen.getByText("v0.9.0")).toBeTruthy());
    expect(check).not.toHaveBeenCalled();
  });

  it("renders the chrome without a version when the server cannot answer", async () => {
    // A box with no answer must still get a usable console, and must not show
    // a placeholder where a version belongs. #663's rule, one component over.
    vi.spyOn(api, "version").mockRejectedValue(new Error("no"));
    renderChrome();
    await waitFor(() => expect(screen.getByText("admin")).toBeTruthy());
    expect(screen.queryByText("v0.9.0")).toBeNull();
  });
});
