// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

/* The display zone is read once, when the console starts, and before any page
 * draws a clock. It used to be set as a side effect of api.getSettings, which
 * only five pages call: a cold load onto /jobs drew every timestamp in UTC. */

const fetchMock = vi.fn();

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

const realFetch = globalThis.fetch;
beforeEach(() => {
  globalThis.fetch = fetchMock as unknown as typeof fetch;
});
afterEach(() => {
  cleanup();
  setDisplayTimeZone("UTC");
  fetchMock.mockReset();
  globalThis.fetch = realFetch;
});

function settingsReply(tz: string): Response {
  return new Response(JSON.stringify({ display: { timeZone: tz } }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

describe("the console's display zone at start", () => {
  it("is the stored zone on the first draw of a page that never reads settings", async () => {
    fetchMock.mockResolvedValue(settingsReply("Asia/Tokyo"));
    render(<App />);
    // 2026-01-01T00:00Z is 09:00 in Tokyo; in UTC it would read 00:00:00.
    await screen.findByText(/09:00:00/);
    expect(displayTimeZone()).toBe("Asia/Tokyo");
  });

  it("resets a previous session's zone to UTC, and still opens the console, when settings cannot be read", async () => {
    setDisplayTimeZone("Asia/Tokyo"); // left over from the session that just ended
    fetchMock.mockRejectedValue(new Error("offline"));
    render(<App />);
    await screen.findByText(/00:00:00/);
    expect(displayTimeZone()).toBe("UTC");
  });
});
