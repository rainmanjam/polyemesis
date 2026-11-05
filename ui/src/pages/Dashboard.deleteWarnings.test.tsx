// @vitest-environment jsdom
//
// The 2026-11-04 lingering-state audit (#1, #10). A destination delete now
// cancels the destination's scheduled platform events and prunes its id from
// schedules, and says in `warnings` what it could not take with it -- an event
// left on the platform, a schedule it switched off. The dashboard says it after
// the success toast, in the server's words, and not the reconcile sentence the
// transport already raises.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));
vi.mock("@/components/DestinationDialog", () => ({ DestinationDialog: () => null }));

import { toast } from "sonner";
import { Dashboard } from "./Dashboard";
import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import { api } from "@/lib/api";
import type { Status } from "@/lib/types";

const originalFetch = globalThis.fetch;
const relay = { port: 0, subscribers: [], rxPackets: 0, rxBytes: 0, txPackets: 0, dropped: 0, tsPackets: 0, tsLost: 0, discontinuities: 0, lossPercent: 0 };

const status = {
  renditions: [],
  destinations: [
    {
      id: 5,
      name: "Weekly YouTube",
      kind: "rtmp",
      platform: "youtube",
      enabled: false,
      summary: "",
      tracks: null,
      filterComplex: "",
      normalization: "off",
      warnings: null,
      sourceId: 1,
    },
  ],
  relay,
  source: { id: 1, name: "Studio A", probed: true, tracks: [] },
} as unknown as Status;

const live = {
  programme: 1,
  programmeKnown: true,
  programmes: [{ id: 1, name: "Studio A" }],
  sourceCount: 1,
  connected: true,
  snapshotKnown: true,
  status,
  source: status.source,
  levels: null,
  system: null,
  bitrate: [],
  logs: [],
} as unknown as LiveData;

const routes: Record<string, unknown> = {
  "/api/v1/settings": { preview: { enabled: false }, recording: { enabled: false }, meters: { enabled: false }, failover: { enabled: true } },
  "/api/v1/system": { ingestUrl: "srt://box:6000", ffmpeg: { hasLibsrt: true } },
  "/api/v1/setup": { needsSetup: false, minPasswordChars: 8, sources: 1 },
  "/api/v1/sources": [{ id: 1, name: "Studio A" }],
  "/api/v1/metadata": { targets: [] },
  "/api/v1/metadata/broadcast-window": { accounts: [] },
};

beforeEach(() => {
  if (!Element.prototype.scrollTo) Element.prototype.scrollTo = () => {};
  globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
    const path = String(typeof input === "string" ? input : input.toString()).split("?")[0];
    return { ok: true, status: 200, text: async () => JSON.stringify(routes[path] ?? {}) } as unknown as Response;
  }) as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = originalFetch;
  vi.restoreAllMocks();
  cleanup();
});

describe("Dashboard: deleting a destination says what it left behind", () => {
  it("toasts the delete's own warnings, not the reconcile sentence", async () => {
    const del = vi.spyOn(api, "deleteDestination").mockResolvedValue({
      status: "deleted",
      warnings: ['"Weekly YouTube": the scheduled event yt-1 was left on YouTube (refused).', "reconcile sentence"],
      reconcileFailed: true,
    });
    render(
      <MemoryRouter>
        <LiveDataContext.Provider value={live}>
          <Dashboard />
        </LiveDataContext.Provider>
      </MemoryRouter>,
    );
    const trigger = await screen.findByLabelText(/Weekly YouTube/, { selector: "button[aria-haspopup]" });
    fireEvent.keyDown(trigger, { key: "Enter" });
    fireEvent.click(await screen.findByRole("menuitem", { name: /Delete/ }));
    const dialog = await screen.findByRole("alertdialog").catch(() => screen.findByRole("dialog"));
    fireEvent.click(within(dialog).getByRole("button", { name: /^Delete/ }));

    await waitFor(() => expect(del).toHaveBeenCalledWith(5));
    await waitFor(() =>
      expect(toast.warning).toHaveBeenCalledWith(
        '"Weekly YouTube": the scheduled event yt-1 was left on YouTube (refused).',
        expect.anything(),
      ),
    );
    expect(vi.mocked(toast.warning).mock.calls.some((c) => c[0] === "reconcile sentence")).toBe(false);
  });
});
