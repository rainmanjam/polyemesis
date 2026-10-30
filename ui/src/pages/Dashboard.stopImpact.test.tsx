// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { Dashboard } from "./Dashboard";
import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import { translate } from "@/lib/i18n";
import type { Status } from "@/lib/types";

/* THE STOP ALL DIALOG COUNTS WHAT THE SERVER COUNTS (code review R4).
 *
 * The dialog used to count YouTube broadcasts locally -- every enabled YouTube
 * row -- while the server's stop-impact counts by each row's recorded phase.
 * The two disagreed, and the dialog is what the operator confirms against. It
 * now shows the server's answer, fetched when it opens, and cannot be confirmed
 * until that answer has arrived. A failed count says so and stays shut.
 *
 * Rendered as the whole Dashboard so the test fails if the dialog is not
 * actually wired to the route, not just if the pieces work in isolation. */

const originalFetch = globalThis.fetch;

function row(id: number, name: string, platform: string) {
  return {
    id, name, platform, kind: "rtmp", enabled: true, sourceId: 1, summary: "", tracks: null,
    filterComplex: "", normalization: "off", warnings: null, process: { state: "running" },
  };
}

/** An enabled YouTube row and an enabled Twitch row. A local count says one broadcast ends. */
function status(): Status {
  return {
    renditions: [],
    destinations: [
      row(1, "YT main", "youtube"),
      row(2, "Twitch", "twitch"),
    ],
    relay: { port: 0, subscribers: [], rxPackets: 0, rxBytes: 0, txPackets: 0, dropped: 0, tsPackets: 0, tsLost: 0, discontinuities: 0, lossPercent: 0 },
    source: { id: 1, name: "Main", probed: true, tracks: [] },
  } as unknown as Status;
}

function live(): LiveData {
  return {
    programme: 1,
    programmeKnown: true,
    programmes: [{ id: 1, name: "Main" }],
    connected: true,
    snapshotKnown: true,
    status: status(),
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

/** What GET /destinations/stop-impact does in each test: answer, fail, or never answer. */
let stopImpact: () => Promise<{ status: number; body: unknown }>;
let stopImpactCalls = 0;

beforeEach(() => {
  if (!Element.prototype.scrollTo) Element.prototype.scrollTo = () => {};
  stopImpactCalls = 0;
  globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(typeof input === "string" ? input : input.toString());
    const path = url.split("?")[0];
    if (path === "/api/v1/destinations/stop-impact") {
      stopImpactCalls++;
      const res = await stopImpact();
      return {
        ok: res.status < 300,
        status: res.status,
        text: async () => JSON.stringify(res.body),
      } as unknown as Response;
    }
    return { ok: true, status: 200, text: async () => JSON.stringify(routes[path] ?? {}) } as unknown as Response;
  }) as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = originalFetch;
  vi.restoreAllMocks();
  cleanup();
});

async function openStopAll() {
  render(
    <MemoryRouter>
      <LiveDataContext.Provider value={live()}>
        <Dashboard />
      </LiveDataContext.Provider>
    </MemoryRouter>,
  );
  fireEvent.click(await screen.findByRole("button", { name: new RegExp(translate("en", "dash.stopAll")) }));
  return screen.findByRole("dialog");
}

function confirmButton(dialog: HTMLElement) {
  return within(dialog).getByRole("button", { name: translate("en", "dash.stopAll") }) as HTMLButtonElement;
}

describe("Dashboard: the Stop all dialog's count comes from the server", () => {
  it("shows the server's count, not a local one, and only then can be confirmed", async () => {
    // The server says no broadcast would end (neither row's recorded phase is live).
    stopImpact = async () => ({ status: 200, body: { destinations: 2, youtubeBroadcasts: 0 } });
    const dialog = await openStopAll();

    await within(dialog).findByText(translate("en", "dash.stopAllConsequence", { count: "2" }));
    expect(stopImpactCalls).toBe(1);
    expect(
      within(dialog).queryByText(new RegExp(translate("en", "golive.stopYoutube", { count: 1 }))),
      "the dialog still counted the enabled YouTube row locally",
    ).toBeNull();
    expect(confirmButton(dialog).disabled).toBe(false);
  });

  it("names the server's YouTube count when there is one", async () => {
    stopImpact = async () => ({ status: 200, body: { destinations: 2, youtubeBroadcasts: 1 } });
    const dialog = await openStopAll();
    await within(dialog).findByText(new RegExp(translate("en", "golive.stopYoutube", { count: 1 })));
  });

  it("cannot be confirmed while the count is loading", async () => {
    stopImpact = () => new Promise(() => {});
    const dialog = await openStopAll();
    expect(within(dialog).getByText(translate("en", "dash.stopCountLoading"))).toBeTruthy();
    expect(confirmButton(dialog).disabled).toBe(true);
  });

  it("says the count failed and stays shut when it cannot be loaded", async () => {
    stopImpact = async () => ({ status: 500, body: { error: "store locked" } });
    const dialog = await openStopAll();
    await within(dialog).findByText(new RegExp(translate("en", "dash.stopCountFailed").slice(0, 40)));
    expect(confirmButton(dialog).disabled).toBe(true);
  });
});
