// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

/* THE FIRST-RUN FORM ASKS FOR THE SETUP CODE, AND SENDS IT.
 *
 * POST /api/v1/setup now refuses without the one-time code the server printed
 * at startup (internal/auth/setupcode.go). A form that did not ask for it would
 * leave every fresh install unclaimable from the browser; one that asked but
 * did not send it would fail with a 403 the operator could do nothing about.
 * And the field is useless without the copy saying where the code is, so that
 * is pinned too.
 *
 * Plain functions rather than `vi.fn()` for the API -- see
 * ClipsPage.readState.test.tsx for why a rejecting spy fails its own test. */

const calls: Array<[string, string, string]> = [];

vi.mock("@/lib/api", () => ({
  ApiError: class ApiError extends Error {},
  api: {
    setup: (username: string, password: string, setupCode: string) => {
      calls.push([username, password, setupCode]);
      return Promise.resolve({ username });
    },
    login: () => Promise.resolve({}),
  },
}));

vi.mock("sonner", () => ({ toast: { success: () => {}, error: () => {} } }));

import { AuthScreen } from "./AuthScreen";

afterEach(() => {
  cleanup();
  calls.length = 0;
});

describe("AuthScreen first run", () => {
  it("asks for the setup code, says where it is, and sends it", async () => {
    let done = 0;
    render(<AuthScreen mode="setup" onDone={() => done++} />);
    await act(async () => {});

    const code = screen.getByLabelText("Setup code") as HTMLInputElement;
    expect(code.required).toBe(true);
    expect(screen.getByText(/journalctl -u polyemesis/)).toBeTruthy();
    expect(screen.getByText(/\/data\/setup-code/)).toBeTruthy();

    fireEvent.change(code, { target: { value: "  ABCD-EFGH-JKMN-PQRS " } });
    fireEvent.change(document.getElementById("password")!, { target: { value: "a-long-password" } });
    fireEvent.change(document.getElementById("confirm")!, { target: { value: "a-long-password" } });
    fireEvent.click(screen.getByRole("button", { name: "Create account" }));

    await waitFor(() => expect(done).toBe(1));
    expect(calls).toEqual([["admin", "a-long-password", "ABCD-EFGH-JKMN-PQRS"]]);
  });

  it("does not show the setup code on the sign-in form", async () => {
    render(<AuthScreen mode="login" onDone={() => {}} />);
    await act(async () => {});
    expect(screen.queryByLabelText("Setup code")).toBeNull();
  });
});
