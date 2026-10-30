// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { toast } from "sonner";

import { DangerZone } from "./DangerZone";
import { api } from "@/lib/api";
import { translate } from "@/lib/i18n";
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

  it("removes a platform's app credentials after the platform name is typed, and says so", async () => {
    mock();
    const del = vi.spyOn(api, "deleteCreds").mockResolvedValue({ status: "ok" } as never);
    const ok = vi.spyOn(toast, "success").mockImplementation(() => "");
    render(<DangerZone />);
    await screen.findByText(/Connected accounts using this app: 1\./);
    fireEvent.click(screen.getByRole("button", { name: "Remove credentials" }));
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "twitch" } });
    fireEvent.click(screen.getAllByRole("button", { name: "Remove credentials" }).at(-1) as HTMLButtonElement);
    await waitFor(() => expect(del).toHaveBeenCalledWith("twitch"));
    await waitFor(() => expect(ok).toHaveBeenCalled());
  });

  it("shows the server's own refusal when the credentials cannot be removed", async () => {
    mock();
    vi.spyOn(api, "deleteCreds").mockRejectedValue(new Error("2 accounts still use this app"));
    const err = vi.spyOn(toast, "error").mockImplementation(() => "");
    render(<DangerZone />);
    await screen.findByText(/Connected accounts using this app: 1\./);
    fireEvent.click(screen.getByRole("button", { name: "Remove credentials" }));
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "twitch" } });
    fireEvent.click(screen.getAllByRole("button", { name: "Remove credentials" }).at(-1) as HTMLButtonElement);
    await waitFor(() => expect(err).toHaveBeenCalledWith("2 accounts still use this app"));
  });

  it("reports a partial revoke as the number that went", async () => {
    const revoke = mock();
    revoke.mockRejectedValueOnce(new Error("gone"));
    const warn = vi.spyOn(toast, "warning").mockImplementation(() => "");
    render(<DangerZone />);
    await screen.findByText(/Active tokens: 2\./);
    fireEvent.click(screen.getByRole("button", { name: "Revoke all tokens" }));
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "all tokens" } });
    fireEvent.click(screen.getAllByRole("button", { name: "Revoke all tokens" }).at(-1) as HTMLButtonElement);
    await waitFor(() => expect(warn).toHaveBeenCalled());
  });

  it("says when the counts could not be loaded", async () => {
    vi.spyOn(api, "listTokens").mockRejectedValue(new Error("offline"));
    vi.spyOn(api, "listCreds").mockResolvedValue([]);
    vi.spyOn(api, "listAccounts").mockResolvedValue([]);
    render(<DangerZone />);
    expect(await screen.findByText(translate("en", "danger.loadFailed"))).toBeTruthy();
  });
});
