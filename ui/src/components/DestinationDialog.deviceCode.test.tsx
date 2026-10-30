// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* Connecting an account from inside the add form, where the platform has a
 * device flow. The code path stays on this page, so the operator does not lose
 * the form they are halfway through; the redirect is the fallback.
 *
 * THE PAIRED ASSERTION IS THE NEGATIVE ONE. A platform whose setup guide does
 * not declare a device flow must not be offered a code, because a button that
 * cannot work is the false promise this change exists to remove.
 */

const guides = vi.hoisted(() => ({
  value: [] as unknown[],
  creds: [] as unknown[],
  // Starts empty; the connect flow adds the account, as the server would.
  accounts: [] as unknown[],
}));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      listSources: () => Promise.resolve([{ id: 1, name: "Main", publishUrls: {}, isDefault: true }]),
      listAccounts: () => Promise.resolve(guides.accounts),
      listRenditions: () => Promise.resolve([]),
      listPlatformPresets: () => Promise.resolve([]),
      startDeviceAuth: () =>
        Promise.resolve({
          handle: "h",
          userCode: "ABCD-1234",
          verificationUri: "https://www.twitch.tv/activate",
          expiresAt: new Date(Date.now() + 600_000).toISOString(),
          intervalSeconds: 5,
        }),
      pollDeviceAuth: () => {
        guides.accounts = [{ id: 7, platform: "twitch", accountName: "streamer" }];
        return Promise.resolve({ state: "connected", account: { id: 7, platform: "twitch", accountName: "streamer" } });
      },
      platformGuides: () => Promise.resolve(guides.value),
      listCreds: () => Promise.resolve(guides.creds),
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
  const view = render(
    <DestinationDialog open onOpenChange={() => {}} destination={twitch as never} onSaved={() => {}} />,
    { wrapper: MemoryRouter },
  );
  // Connecting an account is step 2 of the dialog (A6).
  fireEvent.click(screen.getByTestId("dest-step-2"));
  return view;
}

describe("DestinationDialog, connecting an account in place", () => {
  it("offers Connect with a code when the guide declares a device flow and credentials are stored", async () => {
    guides.value = [{ platform: "twitch", name: "Twitch", deviceFlow: true }];
    guides.creds = [{ platform: "twitch", clientId: "abc", hasSecret: true, updatedAt: "" }];
    open();
    await waitFor(() => expect(screen.getByRole("button", { name: "Connect with a code" })).toBeTruthy());
  });

  it("does not offer a code without developer credentials, because the start would be refused", async () => {
    // The server answers a device start with "no twitch developer credentials
    // configured" when none are stored. A primary button that fails that way
    // is the promise this change must not make, so the gate is the credentials.
    guides.value = [{ platform: "twitch", name: "Twitch", deviceFlow: true }];
    guides.creds = [];
    open();
    await waitFor(() => expect(screen.getByText(/Connect Twitch account/)).toBeTruthy());
    expect(screen.queryByRole("button", { name: "Connect with a code" })).toBeNull();
  });

  it("does not offer a code where the platform has no device flow", async () => {
    guides.value = [{ platform: "twitch", name: "Twitch", deviceFlow: false }];
    guides.creds = [{ platform: "twitch", clientId: "abc", hasSecret: true, updatedAt: "" }];
    open();
    // The redirect fallback is still there, so the account row is rendered.
    await waitFor(() => expect(screen.getByText(/Connect Twitch account/)).toBeTruthy());
    expect(screen.queryByRole("button", { name: "Connect with a code" })).toBeNull();
  });

  it("keeps the form and refreshes the account list when connected in place", async () => {
    // The point of doing this in place: the operator is halfway through the
    // form, and a redirect would have thrown that away. Here the name they typed
    // survives the whole flow, and the connected account replaces the code
    // button without a reload of the dialog.
    guides.value = [{ platform: "twitch", name: "Twitch", deviceFlow: true }];
    guides.creds = [{ platform: "twitch", clientId: "abc", hasSecret: true, updatedAt: "" }];
    guides.accounts = [];
    open();
    await waitFor(() => expect(screen.getByRole("button", { name: "Connect with a code" })).toBeTruthy());

    fireEvent.change(screen.getByLabelText(/^Name$/i), { target: { value: "Renamed" } });
    fireEvent.click(screen.getByRole("button", { name: "Connect with a code" }));

    // The code is on screen, inside the same dialog.
    await waitFor(() => expect(screen.getByText("ABCD-1234")).toBeTruthy());

    // Connected: the account list is re-read and the picker takes the place of
    // the code button.
    // The first poll waits the device flow's five-second floor, so this is a
    // real wait rather than a mocked timer: the test is about the sequence.
    await waitFor(() => expect(screen.getByTestId("connected-account-picker")).toBeTruthy(), {
      timeout: 9000,
    });
    expect(screen.queryByRole("button", { name: "Connect with a code" })).toBeNull();

    // And the operator's typing is still there.
    expect((screen.getByLabelText(/^Name$/i) as HTMLInputElement).value).toBe("Renamed");
  }, 15000);
  it("names where the redirect sign-in returns to, before it leaves the form (A5)", async () => {
    guides.value = [
      {
        platform: "twitch",
        name: "Twitch",
        deviceFlow: false,
        redirectPath: "https://box.example/api/v1/oauth/twitch/callback",
        redirectWarnings: ["This host is not reachable from Twitch."],
      },
    ];
    guides.creds = [{ platform: "twitch", clientId: "abc", hasSecret: true, updatedAt: "" }];
    guides.accounts = [];
    open();
    await waitFor(() => expect(screen.getByText(/Connect Twitch account/)).toBeTruthy());
    expect(screen.getByText("https://box.example/api/v1/oauth/twitch/callback")).toBeTruthy();
    expect(screen.getByText("This host is not reachable from Twitch.")).toBeTruthy();
  });
});
