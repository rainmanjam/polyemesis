// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { PairingCheck } from "./PairingCheck";

const checkConnection = vi.fn();
vi.mock("@/lib/api", () => ({
  checkConnection: (...a: unknown[]) => checkConnection(...a),
}));

const readInfo = {
  name: "streamdeck", prefix: "pmk_abcdefgh", scope: "read", adminScope: false,
};

/** Types a token and presses Test connection, the path an operator takes. */
function testToken(token: string) {
  const input = screen.getByLabelText("Token", { exact: true }) as HTMLInputElement;
  fireEvent.change(input, { target: { value: token } });
  fireEvent.click(screen.getByRole("button", { name: /test connection/i }));
}

describe("PairingCheck", () => {
  beforeEach(() => checkConnection.mockReset());
  afterEach(cleanup);

  it("asks the install about the token it was given, and about nothing else", async () => {
    checkConnection.mockResolvedValue({ ok: true, info: readInfo });
    render(<PairingCheck />);
    testToken("  pmk_abcdefgh-secret  ");
    await waitFor(() => expect(checkConnection).toHaveBeenCalledTimes(1));
    expect(checkConnection).toHaveBeenCalledWith("pmk_abcdefgh-secret");
  });

  it("shows the token's name and scope for a read token, with no admin warning", async () => {
    checkConnection.mockResolvedValue({
      ok: true,
      info: { ...readInfo, lastUsedAt: "2026-10-29T10:00:00Z" },
    });
    render(<PairingCheck />);
    testToken("pmk_abcdefgh-secret");
    expect(await screen.findByTestId("pairing-result")).toBeTruthy();
    expect(screen.getByTestId("pairing-result").textContent).toContain("streamdeck");
    expect(screen.queryByTestId("pairing-admin-warning")).toBeNull();
  });

  it("warns when the token is an admin token, since a plugin needs only read", async () => {
    checkConnection.mockResolvedValue({
      ok: true,
      info: { name: "deploy", prefix: "pmk_zzzzzzzz", scope: "admin", adminScope: true },
    });
    render(<PairingCheck />);
    testToken("pmk_zzzzzzzz-secret");
    expect(await screen.findByTestId("pairing-admin-warning")).toBeTruthy();
  });

  it("says a token has never been used rather than showing a date", async () => {
    checkConnection.mockResolvedValue({ ok: true, info: readInfo });
    render(<PairingCheck />);
    testToken("pmk_abcdefgh-secret");
    expect(await screen.findByTestId("pairing-never-used")).toBeTruthy();
  });

  it("reports a refused token as refused, not as a failed connection", async () => {
    checkConnection.mockResolvedValue({ ok: false, reason: "refused" });
    render(<PairingCheck />);
    testToken("pmk_wrong");
    expect(await screen.findByTestId("pairing-refused")).toBeTruthy();
    expect(screen.queryByTestId("pairing-result")).toBeNull();
  });

  it("reports an unreachable install as a failed connection", async () => {
    checkConnection.mockResolvedValue({ ok: false, reason: "unreachable" });
    render(<PairingCheck />);
    testToken("pmk_any");
    expect(await screen.findByTestId("pairing-unreachable")).toBeTruthy();
  });

  it("does not offer a check for an empty token", () => {
    render(<PairingCheck />);
    expect((screen.getByRole("button", { name: /test connection/i }) as HTMLButtonElement).disabled).toBe(true);
  });
});
