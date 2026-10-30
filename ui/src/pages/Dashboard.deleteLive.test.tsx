// @vitest-environment jsdom
//
// A18: deleting a destination that is live right now ends a broadcast, so it
// asks for the name to be typed and says so. An idle one keeps the plain
// confirmation: friction on a reversible delete is how a control becomes a reflex.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { Dashboard } from "./Dashboard";
import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import { translate } from "@/lib/i18n";
import type { Status } from "@/lib/types";

const originalFetch = globalThis.fetch;

let running = true;

function live(): LiveData {
  const status = {
    renditions: [],
    destinations: [
      {
        id: 4, name: "Twitch main", platform: "twitch", kind: "rtmp", enabled: running, sourceId: 1, summary: "",
        tracks: null, filterComplex: "", normalization: "off", warnings: null,
        process: running ? { state: "running", restarts: 0, uptimeSec: 60, progress: {} } : null,
      },
    ],
    relay: { port: 0, subscribers: [], rxPackets: 0, rxBytes: 0, txPackets: 0, dropped: 0, tsPackets: 0, tsLost: 0, discontinuities: 0, lossPercent: 0 },
    source: { id: 1, name: "Main", probed: true, tracks: [] },
  } as unknown as Status;
  return {
    programme: 1,
    programmeKnown: true,
    programmes: [{ id: 1, name: "Main" }],
    connected: true,
    snapshotKnown: true,
    status,
    source: { id: 1, name: "Main", probed: true, tracks: [] },
    levels: null,
    system: null,
    bitrate: [],
    logs: [],
  } as unknown as LiveData;
}

const routes: Record<string, unknown> = {
  "/api/v1/settings": { preview: { enabled: false }, recording: { enabled: false }, meters: { enabled: false }, failover: { enabled: false } },
  "/api/v1/system": { ingestUrl: "rtmp://box/live", ffmpeg: { hasLibsrt: true } },
  "/api/v1/setup": { needsSetup: false, minPasswordChars: 8, sources: 1 },
  "/api/v1/sources": [{ id: 1, name: "Main" }],
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


describe("Dashboard: deleting a live destination (A18)", () => {
  async function openDelete() {
    render(
      <MemoryRouter>
        <LiveDataContext.Provider value={live()}>
          <Dashboard />
        </LiveDataContext.Provider>
      </MemoryRouter>,
    );
    const trigger = await screen.findByRole("button", { name: "Actions for Twitch main" });
    fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false });
    fireEvent.click(await screen.findByRole("menuitem", { name: /Delete/ }));
    return screen.findByRole("dialog");
  }

  it("asks for the name and says the broadcast ends when it is live", async () => {
    running = true;
    const dialog = await openDelete();
    expect(within(dialog).getByText(translate("en", "dash.deleteDestLive"))).toBeTruthy();
    await waitFor(() => expect(within(dialog).getByRole("textbox")).toBeTruthy());
  });

  it("keeps the plain confirmation when it is not live", async () => {
    running = false;
    const dialog = await openDelete();
    expect(within(dialog).queryByRole("textbox")).toBeNull();
  });
});
