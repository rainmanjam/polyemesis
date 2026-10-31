// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { translate } from "@/lib/i18n";

/* A FAILED SIGN-IN OR SETUP TOOK THE OPERATOR'S PASSWORD WITH IT. (A75, A76, A77)
 *
 *   A75  Every failure cleared both password fields, and every validation error
 *        was a toast that vanished. A wrong setup code now keeps what was typed:
 *        only a rejected password (401) clears it. Validation is shown inline, by
 *        the field it is about, and the mismatch shows under Confirm while typing.
 *
 *   A76  The setup-code help was a dense 10px paragraph covering four commands with
 *        no hint about which one applies. It is now a disclosure, with each route
 *        on its own line, and nothing on this screen is set below 12px.
 *
 *   A77  The strength line was a live region holding the whole sentence, so every
 *        keystroke re-announced it. The live region now holds only the strength word,
 *        and it changes only when the strength does.
 *
 * Plain functions for the API, not a rejecting vi.fn(): see ClipsPage.readState. */

const attempts: Array<{ kind: "ok" | "401" | "403" | "network" }> = [];
const sent: string[][] = [];

vi.mock("@/lib/api", () => {
  class ApiError extends Error {
    readonly status: number;
    constructor(status: number, message: string) {
      super(message);
      this.status = status;
    }
  }
  const next = () => attempts.shift() ?? { kind: "ok" as const };
  return {
    ApiError,
    api: {
      setup: (username: string, password: string, code: string) => {
        sent.push(["setup", username, password, code]);
        const a = next();
        if (a.kind === "403") return Promise.reject(new ApiError(403, "setup code rejected"));
        if (a.kind === "network") return Promise.reject(new TypeError("Failed to fetch"));
        return Promise.resolve({ username });
      },
      login: (username: string, password: string) => {
        sent.push(["login", username, password]);
        const a = next();
        if (a.kind === "401") return Promise.reject(new ApiError(401, "invalid credentials"));
        if (a.kind === "network") return Promise.reject(new TypeError("Failed to fetch"));
        return Promise.resolve({ username });
      },
    },
  };
});

vi.mock("sonner", () => ({
  toast: { success: () => {}, error: () => {} },
}));

const { AuthScreen } = await import("./AuthScreen");

afterEach(() => {
  cleanup();
  attempts.length = 0;
  sent.length = 0;
});

const LONG = "correct horse battery";

function fillSetup(password: string, confirm: string, code = "ABCD-EFGH-JKMN-PQRS") {
  fireEvent.change(screen.getByLabelText(translate("en", "auth.setupCode")), { target: { value: code } });
  fireEvent.change(screen.getByLabelText(translate("en", "auth.password")), { target: { value: password } });
  fireEvent.change(screen.getByLabelText(translate("en", "auth.confirmPassword")), { target: { value: confirm } });
}

describe("AuthScreen: a failed attempt keeps what the operator typed (A75)", () => {
  it("keeps the password when the setup code is wrong", async () => {
    attempts.push({ kind: "403" });
    render(<AuthScreen mode="setup" onDone={() => {}} />);
    fillSetup(LONG, LONG, "WRONG-CODE");

    fireEvent.click(screen.getByRole("button", { name: translate("en", "auth.createAccount") }));

    await waitFor(() => expect(sent.length).toBe(1));
    expect((screen.getByLabelText(translate("en", "auth.password")) as HTMLInputElement).value).toBe(LONG);
  });

  it("keeps the password when the connection drops", async () => {
    attempts.push({ kind: "network" });
    render(<AuthScreen mode="login" onDone={() => {}} />);
    fireEvent.change(screen.getByLabelText(translate("en", "auth.password")), { target: { value: LONG } });

    fireEvent.click(screen.getByRole("button", { name: translate("en", "auth.signIn") }));

    await waitFor(() => expect(sent.length).toBe(1));
    expect((screen.getByLabelText(translate("en", "auth.password")) as HTMLInputElement).value).toBe(LONG);
  });

  it("still clears the password when the server rejects it, because it must be retyped", async () => {
    attempts.push({ kind: "401" });
    render(<AuthScreen mode="login" onDone={() => {}} />);
    fireEvent.change(screen.getByLabelText(translate("en", "auth.password")), { target: { value: LONG } });

    fireEvent.click(screen.getByRole("button", { name: translate("en", "auth.signIn") }));

    await waitFor(() => expect(sent.length).toBe(1));
    expect((screen.getByLabelText(translate("en", "auth.password")) as HTMLInputElement).value).toBe("");
  });

  it("shows the mismatch under Confirm while typing, and sends nothing", () => {
    render(<AuthScreen mode="setup" onDone={() => {}} />);
    fillSetup(LONG, "correct horse");

    const confirm = screen.getByLabelText(translate("en", "auth.confirmPassword"));
    expect(confirm.getAttribute("aria-invalid")).toBe("true");
    expect(screen.getByText(translate("en", "auth.passwordMismatch"))).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: translate("en", "auth.createAccount") }));
    expect(sent.length).toBe(0);
  });
});

describe("AuthScreen: the setup-code help is readable (A76)", () => {
  it("tucks the routes behind a disclosure with each one on its own line", () => {
    render(<AuthScreen mode="setup" onDone={() => {}} />);

    const summary = screen.getByText(translate("en", "auth.setupCodeWhere"));
    expect(summary.tagName).toBe("SUMMARY");
    expect(screen.getByText(/journalctl -u polyemesis/)).toBeTruthy();
    expect(screen.getByText(/docker logs polyemesis/)).toBeTruthy();
  });

  it("uses no text below the 12px floor on this screen", () => {
    render(<AuthScreen mode="setup" onDone={() => {}} />);

    expect(document.querySelectorAll(".text-\\[10px\\]").length).toBe(0);
  });
});

describe("AuthScreen: the strength line is announced on change of strength only (A77)", () => {
  it("keeps the length sentence out of the live region", () => {
    render(<AuthScreen mode="setup" onDone={() => {}} />);
    fireEvent.change(screen.getByLabelText(translate("en", "auth.password")), { target: { value: "abcdefgh" } });

    const live = document.querySelector('[aria-live="polite"]');
    expect(live).not.toBeNull();
    expect(live!.textContent).not.toContain(translate("en", "i18nSweep.passwordMin", { count: 8 }));
  });

  it("announces the word for the strength, which changes on a change of tier", () => {
    render(<AuthScreen mode="setup" onDone={() => {}} />);
    const field = screen.getByLabelText(translate("en", "auth.password"));
    fireEvent.change(field, { target: { value: "abcdefgh" } });
    expect(document.querySelector('[aria-live="polite"]')!.textContent).toContain(
      translate("en", "i18nSweep.strengthWeak"),
    );

    // Four character classes and 22 characters: the top tier.
    fireEvent.change(field, { target: { value: "Correct-Horse-Battery9" } });
    expect(document.querySelector('[aria-live="polite"]')!.textContent).toContain(
      translate("en", "i18nSweep.strengthStrong"),
    );
  });
});
