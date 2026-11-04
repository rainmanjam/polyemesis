// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { toast } from "sonner";

import { DangerZone } from "./DangerZone";
import { api } from "@/lib/api";
import { setLanguage } from "@/lib/i18n";
import type { ApiToken, PlatformAccount, PlatformCreds } from "@/lib/types";

/* The warning after "Revoke all tokens" partly fails. With one token there is
 * one to name, so it is "token" and "It is still active", not "tokens" and
 * "The rest are still active". */

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  setLanguage("en");
});

/** `count` tokens, the first of which the server refuses to revoke. */
async function revokeAllWithOneFailure(count: number) {
  const tokens = Array.from({ length: count }, (_, i) => ({ id: i + 1 })) as ApiToken[];
  vi.spyOn(api, "listTokens").mockResolvedValue(tokens);
  vi.spyOn(api, "listCreds").mockResolvedValue([] as PlatformCreds[]);
  vi.spyOn(api, "listAccounts").mockResolvedValue([] as PlatformAccount[]);
  vi.spyOn(api, "revokeToken").mockResolvedValue({ status: "ok" }).mockRejectedValueOnce(new Error("gone"));
  const warn = vi.spyOn(toast, "warning").mockImplementation(() => "");
  render(<DangerZone />);
  await screen.findByText(new RegExp(`Active tokens: ${count}\\.`));
  fireEvent.click(screen.getByRole("button", { name: "Revoke all tokens" }));
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "all tokens" } });
  fireEvent.click(screen.getAllByRole("button", { name: "Revoke all tokens" }).at(-1) as HTMLButtonElement);
  await waitFor(() => expect(warn).toHaveBeenCalled());
  return warn;
}

describe("DangerZone: danger.tokensPartial (en)", () => {
  it("says 0 of 1 token and that it is still active, when the only token fails", async () => {
    const warn = await revokeAllWithOneFailure(1);
    expect(warn).toHaveBeenCalledWith("Revoked 0 of 1 token. It is still active; see the API tokens card.");
    expect(warn).not.toHaveBeenCalledWith(
      "Revoked 0 of 1 tokens. The rest are still active; see the API tokens card.",
    );
  });

  it("says 1 of 2 tokens and that the rest are still active, when one of two fails", async () => {
    const warn = await revokeAllWithOneFailure(2);
    expect(warn).toHaveBeenCalledWith("Revoked 1 of 2 tokens. The rest are still active; see the API tokens card.");
  });
});
