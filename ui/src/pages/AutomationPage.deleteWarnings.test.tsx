// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

import { toast } from "sonner";
import { AutomationPage } from "./AutomationPage";
import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import { translate } from "@/lib/i18n";

/* The 2026-11-04 lingering-state audit, #1 part B. Deleting a schedule now
 * withdraws the events it pre-announced. Facebook has no call to remove one,
 * so the delete answers `warnings` naming the event to remove by hand -- and
 * this page used to throw the response away. */

const originalFetch = globalThis.fetch;
const LEFT = '"fb": the scheduled event fb-video-2 was left on Facebook (no schedule will air the show).';

const schedule = {
  id: 9,
  name: "Friday show",
  enabled: true,
  action: "start",
  kind: "weekly",
  destinationIds: [3],
  allDestinations: false,
  tz: "UTC",
  atMinutes: 1140,
  days: [5],
  runAt: "1970-01-01T00:00:00Z",
  graceSeconds: 300,
  lastRunAt: "1970-01-01T00:00:00Z",
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
  localTime: "19:00",
};

beforeEach(() => {
  globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(typeof input === "string" ? input : input.toString()).split("?")[0];
    let body: unknown = {};
    if (path === "/api/v1/schedules") body = [schedule];
    else if (path === "/api/v1/schedules/runs") body = [];
    else if (path === "/api/v1/alerts/rules") body = [];
    else if (path === "/api/v1/schedules/9" && init?.method === "DELETE") body = { status: "deleted", warnings: [LEFT] };
    return { ok: true, status: 200, text: async () => JSON.stringify(body) } as unknown as Response;
  }) as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = originalFetch;
  vi.clearAllMocks();
  cleanup();
});

describe("AutomationPage: deleting a schedule says what it could not withdraw", () => {
  it("toasts the delete's warnings", async () => {
    // The Schedules tab, as the docs link to it.
    window.history.replaceState(null, "", "/automation?tab=schedules");
    render(
      <LiveDataContext.Provider
        value={{ programme: 1, programmeKnown: true, snapshotKnown: true, status: null, source: null, bitrate: [], logs: [] } as unknown as LiveData}
      >
        <AutomationPage />
      </LiveDataContext.Provider>,
    );
    fireEvent.click(await screen.findByRole("button", { name: translate("en", "auto.delete") }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: translate("en", "auto.deleteSchedule") }));
    await waitFor(() => expect(toast.warning).toHaveBeenCalledWith(LEFT, expect.anything()));
  });
});
