// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { DangerZone } from "./DangerZone";
import { api } from "@/lib/api";
import type { ApiToken, PlatformAccount, PlatformCreds } from "@/lib/types";

/* THE DANGER ZONE (A17): the irreversible actions, counted, each behind a typed
 * confirmation. */
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function mock() {
  vi.spyOn(api, "listTokens").mockResolvedValue([{ id: 1 }, { id: 2 }] as ApiToken[]);
  vi.spyOn(api, "listCreds").mockResolvedValue([{ platform: "twitch" }] as PlatformCreds[]);
  vi.spyOn(api, "listAccounts").mockResolvedValue([
    { platform: "twitch" },
    { platform: "youtube" },
  ] as PlatformAccount[]);
  return vi.spyOn(api, "revokeToken").mockResolvedValue({ status: "ok" });
}

describe("DangerZone", () => {
  it("counts the tokens and the accounts each platform app carries", async () => {
    mock();
    render(<DangerZone />);
    expect(await screen.findByText(/Active tokens: 2\./)).toBeTruthy();
    expect(screen.getByText(/Connected accounts using this app: 1\./)).toBeTruthy();
  });

  it("revokes every token only after the subject is typed", async () => {
    const revoke = mock();
    render(<DangerZone />);
    await screen.findByText(/Active tokens: 2\./);
    fireEvent.click(screen.getByRole("button", { name: "Revoke all tokens" }));
    const confirm = () => screen.getAllByRole("button", { name: "Revoke all tokens" }).at(-1) as HTMLButtonElement;
    expect(confirm().disabled).toBe(true);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "all tokens" } });
    fireEvent.click(confirm());
    await waitFor(() => expect(revoke).toHaveBeenCalledTimes(2));
  });
});
