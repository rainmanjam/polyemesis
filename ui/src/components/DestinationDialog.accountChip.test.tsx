// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { AccountChip } from "./DestinationDialog";
import type { PlatformAccount } from "@/lib/types";

/* THE LINKED ACCOUNT, AND HOW IT IS DOING (A5). The server's health field
 * reaches the add-destination flow, so a failing refresh is read before a
 * broadcast needs the token. */
afterEach(cleanup);

const account = (health: PlatformAccount["health"]): PlatformAccount =>
  ({ id: 7, platform: "twitch", accountName: "streamer", expiresAt: "2026-11-01T10:00:00Z", health }) as PlatformAccount;

function draw(a: PlatformAccount, onUnlink = () => {}) {
  return render(<AccountChip account={a} onUnlink={onUnlink} />, { wrapper: MemoryRouter });
}

describe("AccountChip", () => {
  it("says a healthy token renews itself and offers no expiry to worry about", () => {
    draw(account({ state: "ok" }));
    const chip = screen.getByTestId("account-chip").textContent ?? "";
    expect(chip).toContain("streamer");
    expect(chip).toContain("Token renews automatically");
    expect(chip).not.toContain("until");
  });

  it("names a failing refresh with the platform's own words", () => {
    draw(account({ state: "refreshFailing", failures: 3, lastError: "invalid_grant: token revoked" }));
    const chip = screen.getByTestId("account-chip").textContent ?? "";
    expect(chip).toContain("Failing to renew its token");
    expect(chip).toContain("invalid_grant: token revoked");
  });

  it("unlinks from this destination and points to Settings to manage", () => {
    const onUnlink = vi.fn();
    draw(account({ state: "expiring" }), onUnlink);
    fireEvent.click(screen.getByRole("button", { name: "Unlink" }));
    expect(onUnlink).toHaveBeenCalledOnce();
    expect(screen.getByRole("link", { name: "Manage accounts" }).getAttribute("href")).toBe("/settings?tab=platforms");
  });
});
