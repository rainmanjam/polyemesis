// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";

/* Connecting an account from inside the add form, where the platform has a
 * device flow. The code path stays on this page, so the operator does not lose
 * the form they are halfway through; the redirect is the fallback.
 *
 * THE PAIRED ASSERTION IS THE NEGATIVE ONE. A platform whose setup guide does
 * not declare a device flow must not be offered a code, because a button that
 * cannot work is the false promise this change exists to remove.
 */

const guides = vi.hoisted(() => ({ value: [] as unknown[] }));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      listSources: () => Promise.resolve([{ id: 1, name: "Main", publishUrls: {}, isDefault: true }]),
      listAccounts: () => Promise.resolve([]),
      listRenditions: () => Promise.resolve([]),
      listPlatformPresets: () => Promise.resolve([]),
      platformGuides: () => Promise.resolve(guides.value),
      services: () => Promise.resolve({}),
      listServices: () => Promise.resolve({ services: [] }),
    },
  };
});

import { DestinationDialog } from "./DestinationDialog";

beforeAll(() => {
  (window as unknown as { ResizeObserver?: unknown }).ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.releasePointerCapture = () => {};
  window.HTMLElement.prototype.setPointerCapture = () => {};
  window.HTMLElement.prototype.scrollIntoView = () => {};
});

afterEach(cleanup);

const twitch = {
  id: 9,
  name: "Main",
  kind: "rtmp",
  platform: "twitch",
  url: "rtmp://ingest.example/live",
  streamKey: "k",
  audioBitrate: 160,
  sourceId: 1,
};

function open() {
  return render(
    <DestinationDialog open onOpenChange={() => {}} destination={twitch as never} onSaved={() => {}} />,
  );
}

describe("DestinationDialog, connecting an account in place", () => {
  it("offers Connect with a code when the setup guide declares a device flow", async () => {
    guides.value = [{ platform: "twitch", name: "Twitch", deviceFlow: true }];
    open();
    await waitFor(() => expect(screen.getByRole("button", { name: "Connect with a code" })).toBeTruthy());
  });

  it("does not offer a code where the platform has no device flow", async () => {
    guides.value = [{ platform: "twitch", name: "Twitch", deviceFlow: false }];
    open();
    // The redirect fallback is still there, so the account row is rendered.
    await waitFor(() => expect(screen.getByText(/Connect Twitch account/)).toBeTruthy());
    expect(screen.queryByRole("button", { name: "Connect with a code" })).toBeNull();
  });
});
