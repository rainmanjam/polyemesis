// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

import { PlatformCredCard } from "./SettingsPage";
import { ACCOUNT_IN_USE, ApiError } from "@/lib/api";
import type { PlatformAccount, SetupGuide } from "@/lib/types";

/* THE DISCONNECT BUTTON, AND THE 409 THAT IS THE ORDINARY ANSWER TO IT.
 *
 * The server refuses an unconfirmed DELETE /platforms/accounts/{id} whenever a
 * destination on that account is ENABLED or mid-broadcast -- and a normal
 * install leaves its destinations enabled, so the refusal is not an edge case
 * but the answer most operators get. A client that has no branch for it turns
 * the refusal into a dead button: the request goes out, the 409 comes back, and
 * the operator sees nothing at all.
 *
 * Three things are asserted, and each one alone passes while the feature is
 * broken:
 *
 *   1. The refusal is SHOWN, naming the destinations the server named. A toast
 *      saying "request failed (409)" is not showing it.
 *   2. Confirming RE-SENDS with {"confirm": true}. A dialog whose confirm
 *      button repeats the same unconfirmed request loops forever.
 *   3. Nothing is disconnected on the refusal itself -- no success toast, no
 *      reload -- because nothing was deleted.
 */

const deleteAccount = vi.fn();
const confirmDeleteAccount = vi.fn();
const accountStats = vi.fn(async () => ({ supported: false, reason: "not in this test" }));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      deleteAccount: (...a: unknown[]) => deleteAccount(...a),
      confirmDeleteAccount: (...a: unknown[]) => confirmDeleteAccount(...a),
      accountStats: (...a: unknown[]) => accountStats(...(a as [])),
      connectUrl: (p: string) => `/api/v1/oauth/${p}/start`,
      putCreds: vi.fn(),
      deleteCreds: vi.fn(),
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
  platform: "youtube",
  name: "YouTube",
  consoleUrl: "",
  redirectPath: "/api/v1/oauth/youtube/callback",
  steps: [],
  scopes: null,
  supported: true,
};

const account: PlatformAccount = {
  id: 7,
  platform: "youtube",
  accountName: "Main channel",
  accountRef: "UC123",
  expiresAt: "2026-01-01T00:00:00Z",
  scopes: "",
  scopeVer: 1,
  createdAt: "2026-01-01T00:00:00Z",
  updatedAt: "2026-01-01T00:00:00Z",
};

/** The exact body internal/api/oauth_handlers.go answers an unconfirmed
 *  disconnect with while a destination is enabled. */
function inUse(): ApiError {
  return new ApiError(
    409,
    "2 destinations are still on this connected account: Main YouTube, Backup. " +
      'Send this request again with {"confirm": true} to do it anyway.',
    ACCOUNT_IN_USE,
    [
      {
        id: 1,
        name: "Main YouTube",
        platform: "youtube",
        enabled: true,
        broadcastId: "abc",
        phase: "live",
        broadcasting: true,
      },
      { id: 2, name: "Backup", platform: "youtube", enabled: true, broadcasting: false },
    ],
  );
}

const onChanged = vi.fn();

function renderCard() {
  return render(
    <PlatformCredCard
      guide={guide}
      creds={{ platform: "youtube", clientId: "id", hasSecret: true, updatedAt: "" }}
      accounts={[account]}
      onChanged={onChanged}
    />,
  );
}

/** Click Disconnect and get past the ordinary "are you sure" dialog, which is
 *  not what this file is about. */
async function pressDisconnectAndConfirm() {
  fireEvent.click(screen.getByLabelText("Disconnect"));
  const first = await screen.findByRole("dialog");
  fireEvent.click(within(first).getByRole("button", { name: "Disconnect" }));
}

beforeEach(() => {
  deleteAccount.mockReset();
  confirmDeleteAccount.mockReset();
  toastSuccess.mockReset();
  toastError.mockReset();
  onChanged.mockReset();
});
afterEach(cleanup);

describe("disconnecting an account the install is still using", () => {
  it("shows the refusal, naming the destinations the server named", async () => {
    deleteAccount.mockRejectedValue(inUse());
    renderCard();
    await pressDisconnectAndConfirm();

    await waitFor(() => {
      expect(deleteAccount).toHaveBeenCalledWith(7);
    });

    // WHICH destinations, by name. A count is not something an operator can
    // act on; "Main YouTube" is.
    await waitFor(() => {
      expect(document.body.textContent).toContain("Main YouTube");
    });
    expect(document.body.textContent).toContain("Backup");
    // And the server's own sentence, not a client-side paraphrase of it.
    expect(document.body.textContent).toContain("still on this connected account");

    // Nothing was deleted, so nothing may say it was.
    expect(toastSuccess).not.toHaveBeenCalled();
    expect(onChanged).not.toHaveBeenCalled();
  });

  it("re-sends the delete confirmed when the operator says go ahead", async () => {
    deleteAccount.mockRejectedValue(inUse());
    confirmDeleteAccount.mockResolvedValue({ status: "disconnected" });
    renderCard();
    await pressDisconnectAndConfirm();

    await waitFor(() => {
      expect(document.body.textContent).toContain("Main YouTube");
    });

    // requireTyping: this cannot be undone -- reconnecting mints a new row id
    // and the destinations' account_id is already NULL -- so the account name
    // has to be typed before the override unlocks.
    const input = document.getElementById("confirm-subject") as HTMLInputElement;
    expect(input, "the override dialog must ask for the account name").toBeTruthy();
    fireEvent.change(input, { target: { value: "Main channel" } });

    const anyway = Array.from(document.querySelectorAll("button")).find((b) =>
      /anyway/i.test(b.textContent ?? ""),
    )!;
    expect(anyway, "no 'disconnect anyway' button").toBeTruthy();
    fireEvent.click(anyway);

    await waitFor(() => {
      expect(confirmDeleteAccount).toHaveBeenCalledWith(7);
    });
    await waitFor(() => {
      expect(onChanged).toHaveBeenCalled();
    });
    expect(toastSuccess).toHaveBeenCalled();
  });

  it("reports a failed override rather than closing over it in silence", async () => {
    // The override is the one press where a silent failure is worst: the
    // operator has read the list, typed the account name and decided. A dialog
    // that closed with nothing said would read as "done".
    deleteAccount.mockRejectedValue(inUse());
    confirmDeleteAccount.mockRejectedValue(new ApiError(500, "the store is unwell"));
    renderCard();
    await pressDisconnectAndConfirm();
    await waitFor(() => {
      expect(document.body.textContent).toContain("Main YouTube");
    });

    const input = document.getElementById("confirm-subject") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "Main channel" } });
    fireEvent.click(
      Array.from(document.querySelectorAll("button")).find((b) =>
        /anyway/i.test(b.textContent ?? ""),
      )!,
    );

    await waitFor(() => {
      expect(toastError).toHaveBeenCalledWith("the store is unwell");
    });
    // Nothing was disconnected, so nothing may say it was.
    expect(toastSuccess).not.toHaveBeenCalled();
    expect(onChanged).not.toHaveBeenCalled();
  });

  it("reports an ordinary failure rather than swallowing it", async () => {
    deleteAccount.mockRejectedValue(new ApiError(500, "the database is unwell"));
    renderCard();
    await pressDisconnectAndConfirm();

    await waitFor(() => {
      expect(toastError).toHaveBeenCalledWith("the database is unwell");
    });
    expect(confirmDeleteAccount).not.toHaveBeenCalled();
  });
});
