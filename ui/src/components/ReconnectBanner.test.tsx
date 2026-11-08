// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ReconnectBanner } from "./ReconnectBanner";
import type { PlatformAccount } from "@/lib/types";

const listAccounts = vi.fn();

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      listAccounts: () => listAccounts(),
      connectUrl: (p: string) => `/api/v1/oauth/${p}/start`,
    },
  };
});

function acct(over: Partial<PlatformAccount>): PlatformAccount {
  return {
    id: 1,
    platform: "twitch",
    accountName: "ada",
    accountRef: "ada",
    expiresAt: "",
    scopes: "",
    scopeVer: 5,
    currentScopeVersion: 6,
    scopeUnlocks: ["reconnect.unlock.twitch6"],
    createdAt: "",
    updatedAt: "",
    ...over,
  } as PlatformAccount;
}

describe("ReconnectBanner", () => {
  beforeEach(() => {
    localStorage.clear();
    listAccounts.mockReset();
  });
  afterEach(cleanup);

  it("names the platform and what a reconnect unlocks for a behind account", async () => {
    listAccounts.mockResolvedValue([acct({})]);
    render(<ReconnectBanner />);
    const banner = await screen.findByTestId("reconnect-banner");
    expect(banner.textContent).toContain("Twitch: reconnect to turn on moderation tools");
  });

  it("shows nothing when the account is current", async () => {
    listAccounts.mockResolvedValue([acct({ scopeVer: 6, scopeUnlocks: undefined })]);
    render(<ReconnectBanner />);
    await waitFor(() => expect(listAccounts).toHaveBeenCalled());
    expect(screen.queryByTestId("reconnect-banner")).toBeNull();
  });

  it("shows nothing when the list is refused (non-admin)", async () => {
    listAccounts.mockRejectedValue(new Error("403"));
    render(<ReconnectBanner />);
    await waitFor(() => expect(listAccounts).toHaveBeenCalled());
    expect(screen.queryByTestId("reconnect-banner")).toBeNull();
  });

  it("Reconnect goes to the same OAuth start as Settings", async () => {
    listAccounts.mockResolvedValue([acct({ platform: "x", scopeVer: 1, currentScopeVersion: 2, scopeUnlocks: ["reconnect.unlock.x2"] })]);
    render(<ReconnectBanner />);
    const link = await screen.findByRole("link", { name: "Reconnect X" });
    expect(link.getAttribute("href")).toBe("/api/v1/oauth/x/start");
  });

  it("dismissal is per account and per version", async () => {
    listAccounts.mockResolvedValue([acct({}), acct({ id: 2, platform: "x", accountRef: "x1", scopeVer: 1, currentScopeVersion: 2, scopeUnlocks: ["reconnect.unlock.x2"] })]);
    const first = render(<ReconnectBanner />);
    fireEvent.click(await screen.findByRole("button", { name: "Dismiss the Twitch reconnect notice" }));
    expect(screen.queryByRole("link", { name: "Reconnect Twitch" })).toBeNull();
    // The other account is untouched.
    expect(screen.getByRole("link", { name: "Reconnect X" })).toBeTruthy();
    first.unmount();

    // Remembered across loads for the same version...
    render(<ReconnectBanner />);
    await screen.findByRole("link", { name: "Reconnect X" });
    expect(screen.queryByRole("link", { name: "Reconnect Twitch" })).toBeNull();
    cleanup();

    // ...and back when the build asks for a newer one.
    listAccounts.mockResolvedValue([acct({ currentScopeVersion: 7 })]);
    render(<ReconnectBanner />);
    expect(await screen.findByRole("link", { name: "Reconnect Twitch" })).toBeTruthy();
  });

  it("survives localStorage throwing", async () => {
    listAccounts.mockResolvedValue([acct({})]);
    const spy = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    const set = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    render(<ReconnectBanner />);
    fireEvent.click(await screen.findByRole("button", { name: "Dismiss the Twitch reconnect notice" }));
    expect(screen.queryByTestId("reconnect-banner")).toBeNull();
    spy.mockRestore();
    set.mockRestore();
  });
});
