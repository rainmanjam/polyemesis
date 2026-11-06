// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

/* The display zone is read once, when the console starts, and before any page
 * draws a clock. It used to be set as a side effect of api.getSettings, which
 * only five pages call: a cold load onto /jobs drew every timestamp in UTC. */

const getSettings = vi.fn();

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      setupStatus: vi.fn().mockResolvedValue({ needsSetup: false, minPasswordChars: 12 }),
      me: vi.fn().mockResolvedValue({ username: "admin" }),
      getSettings: (...a: unknown[]) => getSettings(...a),
    },
  };
});
vi.mock("@/components/LiveDataProvider", () => ({
  LiveDataProvider: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));
vi.mock("@/pages/AuthScreen", () => ({ AuthScreen: () => <div>the login screen</div> }));

import App from "./App";
import { displayTimeZone, setDisplayTimeZone, timestamp } from "@/lib/format";

// A page that never calls getSettings, standing for /jobs, /clips, /monitoring.
vi.mock("@/components/AppLayout", () => ({
  AppLayout: () => <div>{`at ${timestamp("2026-01-01T00:00:00Z")}`}</div>,
}));

afterEach(() => {
  cleanup();
  setDisplayTimeZone("UTC");
  getSettings.mockReset();
});

describe("the console's display zone at start", () => {
  it("is the stored zone on the first draw of a page that never reads settings", async () => {
    getSettings.mockResolvedValue({ display: { timeZone: "Asia/Tokyo" } });
    render(<App />);
    // 2026-01-01T00:00Z is 09:00 in Tokyo; in UTC it would read 00:00:00.
    await screen.findByText(/09:00:00/);
    expect(displayTimeZone()).toBe("Asia/Tokyo");
  });

  it("falls back to UTC, and still opens the console, when settings cannot be read", async () => {
    getSettings.mockRejectedValue(new Error("403"));
    render(<App />);
    await screen.findByText(/00:00:00/);
    expect(displayTimeZone()).toBe("UTC");
  });
});
