// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* An X destination can be linked to the connected X account. The picker used to
 * be hidden twice over: the X capability row declared no connect id, and the
 * dialog also wanted the "sso" label to read "yes". With the account linked and
 * X Livestream on, the server creates the stream source, so the URL and key may
 * be left empty; with it off they may not (the server refuses, and the copy
 * says why). */

const state = vi.hoisted(() => ({
  gate: "open" as "open" | "closed",
  updates: [] as Array<Record<string, unknown>>,
}));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      listSources: () => Promise.resolve([{ id: 1, name: "Main", publishUrls: {}, isDefault: true }]),
      listAccounts: () =>
        Promise.resolve([{ id: 5, platform: "x", accountName: "RainmanJam", accountRef: "1", expiresAt: null }]),
      listRenditions: () => Promise.resolve([]),
      listPlatformPresets: () => Promise.resolve([]),
      platformGuides: () => Promise.resolve([{ platform: "x", livestreamGate: state.gate }]),
      services: () => Promise.resolve({}),
      updateDestination: (_id: number, body: Record<string, unknown>) => {
        state.updates.push(body);
        return Promise.resolve({ destination: { id: 9, ...body }, warnings: [] });
      },
    },
  };
});

import { DestinationDialog } from "./DestinationDialog";
import en from "@/lib/i18n/en.json";

const stored = (over: Record<string, unknown>) => ({
  id: 9,
  name: "X main",
  kind: "rtmp",
  platform: "x",
  url: "",
  streamKey: "",
  accountId: 5,
  sourceId: 1,
  enabled: false,
  ...over,
});

beforeAll(() => {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.releasePointerCapture = () => {};
  window.HTMLElement.prototype.setPointerCapture = () => {};
  window.HTMLElement.prototype.scrollIntoView = () => {};
});

function open(destination: unknown) {
  return render(
    <DestinationDialog open onOpenChange={() => {}} destination={destination as never} onSaved={() => {}} />,
    { wrapper: MemoryRouter },
  );
}

describe("linking an X destination to the connected X account", () => {
  afterEach(() => {
    cleanup();
    state.updates.length = 0;
    state.gate = "open";
  });

  // MUTATION: drop `connect: "x"` from the X row in lib/capabilities.ts, or
  // restore the `supportOf(caps, "sso") === "yes"` term in showOAuth with the
  // row's sso back at "unknown". Observed: FAIL, no picker.
  it("shows the connected-account picker with the X account in it", async () => {
    open(stored({}));
    const picker = await screen.findByTestId("connected-account-picker");
    expect(picker.textContent).toContain("RainmanJam");
  });

  it("says the link fetches the key, not 'type it anyway', when Livestream is on", async () => {
    open(stored({}));
    await screen.findByTestId("connected-account-picker");
    await waitFor(() => expect(screen.getByText(new RegExp(en["addDest.linkFetchesKey"]))).toBeTruthy());
    expect(screen.queryByText(/Type this one in even with an account connected/)).toBeNull();
  });

  it("does not list features it has not verified when Livestream is off", async () => {
    state.gate = "closed";
    open(stored({}));
    await screen.findByTestId("connected-account-picker");
    await waitFor(() => expect(screen.getByText(new RegExp(en["addDest.needsDevCreds"].slice(0, 20)))).toBeTruthy());
    expect(screen.queryByText(/Signing in gets you/)).toBeNull();
    expect(screen.queryByText(new RegExp(en["addDest.linkFetchesKey"]))).toBeNull();
  });

  // The dialog does not refuse an empty URL and key: the server decides, because
  // only it knows whether Livestream is on. MUTATION: add a client-side
  // "URL required" return to saveFromStep. Observed: FAIL, nothing was sent.
  it("sends a linked X destination with an empty URL and key to the server", async () => {
    open(stored({}));
    fireEvent.click(await screen.findByRole("button", { name: en["common.save"] }));
    await waitFor(() => expect(state.updates.length).toBe(1));
    expect(state.updates[0].url).toBe("");
    expect(state.updates[0].accountId).toBe(5);
  });
});
