// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { translate } from "@/lib/i18n";

/* THE SIGN-IN SCREEN SAYS WHICH SERVER IT IS, AND WHETHER IT ANSWERS.
 *
 * The 2026-11 review: the first screen an operator sees was a stock centred
 * card that did not say which box it was signing in to. It now carries the
 * endpoint and the connection state, from what an unauthenticated browser can
 * already know: its own location, and GET /api/v1/health.
 *
 * NOTHING ELSE. /health's per-check `detail` strings can carry database errors
 * and file paths, and the build version is behind sign-in on purpose
 * (GET /version is in the authenticated group). Neither may reach this screen.
 *
 * Plain functions for the API, not a rejecting vi.fn(): see ClipsPage.readState. */

const DETAIL = "the database file is damaged: /var/lib/polyemesis/state.db";
let health: () => Promise<unknown> = () => Promise.resolve({ status: "ok" });

vi.mock("@/lib/api", () => ({
  ApiError: class ApiError extends Error {
    readonly status: number;
    constructor(status: number, message: string) {
      super(message);
      this.status = status;
    }
  },
  api: {
    setup: () => Promise.resolve({}),
    login: () => Promise.resolve({}),
    health: () => health(),
  },
}));

vi.mock("sonner", () => ({ toast: { success: () => {}, error: () => {} } }));

const { AuthScreen } = await import("./AuthScreen");
const { ApiError } = await import("@/lib/api");

afterEach(() => {
  cleanup();
  health = () => Promise.resolve({ status: "ok" });
});

const en = (k: Parameters<typeof translate>[1]) => translate("en", k);

describe("AuthScreen: server identity and connection", () => {
  it("names the endpoint this browser is talking to", async () => {
    render(<AuthScreen mode="login" onDone={() => {}} />);
    expect(screen.getByText(en("auth.server"))).toBeTruthy();
    expect(screen.getByText(window.location.host)).toBeTruthy();
    await waitFor(() => expect(screen.getByText(en("auth.connOk"))).toBeTruthy());
  });

  it("says checking until the health answer arrives", () => {
    health = () => new Promise(() => {});
    render(<AuthScreen mode="login" onDone={() => {}} />);
    expect(screen.getByText(en("auth.connChecking"))).toBeTruthy();
  });

  it("reports a degraded server without printing any check detail", async () => {
    health = () =>
      Promise.resolve({ status: "degraded", checks: [{ name: "database", ok: false, detail: DETAIL }] });
    const { container } = render(<AuthScreen mode="login" onDone={() => {}} />);
    await waitFor(() => expect(screen.getByText(en("auth.connDegraded"))).toBeTruthy());
    expect(container.textContent).not.toContain("damaged");
    expect(container.textContent).not.toContain("/var/lib");
    expect(container.textContent).not.toContain("database");
  });

  it("reads a 503 from /health as reachable but degraded, not as unreachable", async () => {
    health = () => Promise.reject(new ApiError(503, DETAIL));
    const { container } = render(<AuthScreen mode="login" onDone={() => {}} />);
    await waitFor(() => expect(screen.getByText(en("auth.connDegraded"))).toBeTruthy());
    expect(container.textContent).not.toContain("damaged");
  });

  it("reads a network failure as not reachable", async () => {
    health = () => Promise.reject(new TypeError("Failed to fetch"));
    render(<AuthScreen mode="login" onDone={() => {}} />);
    await waitFor(() => expect(screen.getByText(en("auth.connDown"))).toBeTruthy());
  });

  it("shows no build or version string before sign-in", async () => {
    const { container } = render(<AuthScreen mode="login" onDone={() => {}} />);
    await waitFor(() => expect(screen.getByText(en("auth.connOk"))).toBeTruthy());
    expect(container.textContent).not.toMatch(/\bv\d+\.\d+/);
  });
});
