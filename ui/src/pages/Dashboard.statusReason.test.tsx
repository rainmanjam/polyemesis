// @vitest-environment jsdom
//
// B1 on the Dashboard: the Ingest card's failure reads as a StatusReason --
// the reason, the failing step and the as-of line -- rather than a bare error
// string with no step and no time.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { Dashboard } from "./Dashboard";
import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import { translate } from "@/lib/i18n";
import type { Status } from "@/lib/types";

const originalFetch = globalThis.fetch;

let lastError: string | undefined;

function live(): LiveData {
  const status = {
    renditions: [],
    destinations: [],
    ingest: { name: "ingest", kind: "ingest", state: "failed", pid: 0, restarts: 1, uptimeSec: 0, lastError },
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

function draw() {
  render(
    <MemoryRouter>
      <LiveDataContext.Provider value={live()}>
        <Dashboard />
      </LiveDataContext.Provider>
    </MemoryRouter>,
  );
}

describe("Dashboard: the Ingest card's failure names its step and time (B1)", () => {
  it("renders the reason with the ingest step and an as-of line", async () => {
    lastError = "Connection to tcp://0.0.0.0:1935 failed";
    draw();
    expect((await screen.findAllByText("Connection to tcp://0.0.0.0:1935 failed")).length).toBeGreaterThan(0);
    const step = translate("en", "foundation.status.step", {
      step: translate("en", "foundation.status.step.ingest"),
    });
    expect(screen.getByText(step)).toBeTruthy();
    expect(screen.getAllByText(translate("en", "foundation.status.asOfUnknown")).length).toBeGreaterThan(0);
  });

  it("shows no reason block when the server gave none", async () => {
    lastError = undefined;
    draw();
    await screen.findAllByText(translate("en", "chrome.ingest"));
    const step = translate("en", "foundation.status.step", {
      step: translate("en", "foundation.status.step.ingest"),
    });
    expect(screen.queryByText(step)).toBeNull();
  });
});
