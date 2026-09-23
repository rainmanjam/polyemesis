// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";

/* The other half of lib/session.test.ts: that App actually LISTENS. A 401
 * that fires the event into a console nobody subscribed to is the old
 * behaviour with extra steps. */

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      setupStatus: vi.fn().mockResolvedValue({ needsSetup: false, minPasswordChars: 12 }),
      me: vi.fn().mockResolvedValue({ username: "admin" }),
    },
  };
});

// The console's chrome and live socket are beside the point here; what is
// under test is which of the two screens App chooses.
vi.mock("@/components/LiveDataProvider", () => ({
  LiveDataProvider: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));
vi.mock("@/components/AppLayout", () => ({
  AppLayout: () => <div>the console</div>,
}));
vi.mock("@/pages/AuthScreen", () => ({
  AuthScreen: () => <div>the login screen</div>,
}));

import App from "./App";
import { noteResponseStatus } from "@/lib/session";

afterEach(() => cleanup());

describe("App, when the session ends mid-use", () => {
  it("returns to the login screen on a 401", async () => {
    render(<App />);
    await screen.findByText("the console");

    act(() => noteResponseStatus("/destinations", 401));

    await screen.findByText("the login screen");
    expect(screen.queryByText("the console")).toBeNull();
  });

  it("stays put on a wrong current password", async () => {
    render(<App />);
    await screen.findByText("the console");

    act(() => noteResponseStatus("/auth/password", 401));

    expect(screen.getByText("the console")).toBeTruthy();
  });
});
