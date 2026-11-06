// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

import { PlatformCredCard } from "./SettingsPage";
import type { PlatformAccount, SetupGuide } from "@/lib/types";

/* #4 of the 2026-11-04 lingering-state audit. The server now refuses an
 * UNCONFIRMED credentials removal while accounts still use the app
 * (creds_in_use). This card's dialog is a typed confirmation whose copy names
 * the cost, so it sends the confirmed request -- and a failure is a toast, not
 * an unhandled rejection out of ConfirmDestructive (which has no catch). */

const confirmDeleteCreds = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      confirmDeleteCreds: (...a: unknown[]) => confirmDeleteCreds(...a),
      deleteAccount: vi.fn(),
      confirmDeleteAccount: vi.fn(),
      accountStats: vi.fn(async () => ({ supported: false, reason: "not in this test" })),
      connectUrl: (p: string) => `/api/v1/oauth/${p}/start`,
      putCreds: vi.fn(),
      checkCreds: vi.fn(),
    },
  };
});

const toastSuccess = vi.fn();
const toastError = vi.fn();
vi.mock("sonner", () => ({
  toast: {
    success: (...a: unknown[]) => toastSuccess(...a),
    error: (...a: unknown[]) => toastError(...a),
    warning: vi.fn(),
  },
}));

const guide: SetupGuide = {
  platform: "twitch",
  name: "Twitch",
  consoleUrl: "",
  redirectPath: "/api/v1/oauth/twitch/callback",
  steps: [],
  scopes: null,
  supported: true,
};

const account: PlatformAccount = {
  id: 3,
  platform: "twitch",
  accountName: "streamer",
  accountRef: "123",
  expiresAt: "2026-01-01T00:00:00Z",
  scopes: "",
  scopeVer: 1,
  createdAt: "2026-01-01T00:00:00Z",
  updatedAt: "2026-01-01T00:00:00Z",
};

const onChanged = vi.fn();

async function removeThroughTheDialog() {
  render(
    <PlatformCredCard
      guide={guide}
      creds={{ platform: "twitch", clientId: "id", hasSecret: true, updatedAt: "" }}
      accounts={[account]}
      onChanged={onChanged}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Remove" }));
  const dialog = await screen.findByRole("dialog");
  fireEvent.change(within(dialog).getByRole("textbox"), { target: { value: "Twitch" } });
  fireEvent.click(within(dialog).getByRole("button", { name: "Remove credentials" }));
}

beforeEach(() => {
  confirmDeleteCreds.mockReset();
  toastSuccess.mockReset();
  toastError.mockReset();
  onChanged.mockReset();
});
afterEach(cleanup);

describe("removing a platform's credentials from its card", () => {
  it("sends the confirmed removal, since the typed dialog is the confirmation", async () => {
    confirmDeleteCreds.mockResolvedValue({ status: "deleted" });
    await removeThroughTheDialog();
    await waitFor(() => expect(confirmDeleteCreds).toHaveBeenCalledWith("twitch"));
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
  });

  it("toasts a failure instead of letting it escape the dialog", async () => {
    confirmDeleteCreds.mockRejectedValue(new Error("database is locked"));
    await removeThroughTheDialog();
    await waitFor(() => expect(toastError).toHaveBeenCalledWith("database is locked"));
    expect(toastSuccess).not.toHaveBeenCalled();
    expect(onChanged).not.toHaveBeenCalled();
  });
});
