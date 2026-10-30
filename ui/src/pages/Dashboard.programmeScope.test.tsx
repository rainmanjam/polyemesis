// @vitest-environment jsdom
//
// The status socket alternates between every programme's frames. The Ingest
// and Pipeline cards are about the programme selected in the header, so a
// frame from ANOTHER programme arriving last must not replace their figures.
//
// The recapture for the site caught the Pipeline card doing exactly that: the
// header and Ingest card said Main Programme, while the Pipeline card beside
// them read "none -- every destination is on passthrough" and 5 relay
// subscribers, which were programme 2's.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { Dashboard } from "./Dashboard";
import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import { translate } from "@/lib/i18n";
import type { Status } from "@/lib/types";

const originalFetch = globalThis.fetch;

const relay = (subscribers: number) => ({
  port: 0,
  subscribers: Array.from({ length: subscribers }, (_, i) => ({ name: `sub${i}` })),
  rxPackets: 0,
  rxBytes: 0,
  txPackets: 0,
  dropped: 0,
  tsPackets: 0,
  tsLost: 0,
  discontinuities: 0,
  lossPercent: 0,
});

/** Programme 1: a rendition ladder, nine relay subscribers, meters running. */
const frameOne = {
  renditions: [{ id: 1, name: "1080p — primary", consumers: 2, process: { state: "running" } }],
  destinations: [],
  relay: relay(9),
  meters: { name: "meters", kind: "meters", state: "running", pid: 10, restarts: 0, uptimeSec: 30 },
  source: { id: 1, name: "Studio A", probed: true, tracks: [] },
} as unknown as Status;

/** Programme 2: passthrough only, five subscribers, no meters. */
const frameTwo = {
  renditions: [],
  destinations: [],
  relay: relay(5),
  source: { id: 2, name: "Studio B", probed: true, tracks: [] },
} as unknown as Status;

function live(status: Status): LiveData {
  return {
    programme: 1,
    programmeKnown: true,
    programmes: [
      { id: 1, name: "Studio A" },
      { id: 2, name: "Studio B" },
    ],
    connected: true,
    snapshotKnown: true,
    status,
    source: frameOne.source,
    levels: null,
    system: null,
    bitrate: [],
    logs: [],
  } as unknown as LiveData;
}

const routes: Record<string, unknown> = {
  "/api/v1/settings": { preview: { enabled: false }, recording: { enabled: false }, meters: { enabled: true }, failover: { enabled: false } },
  "/api/v1/system": { ingestUrl: "srt://box:6000", ffmpeg: { hasLibsrt: true } },
  "/api/v1/setup": { needsSetup: false, minPasswordChars: 8, sources: 2 },
  "/api/v1/sources": [
    { id: 1, name: "Studio A" },
    { id: 2, name: "Studio B" },
  ],
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

function view(status: Status) {
  return (
    <MemoryRouter>
      <LiveDataContext.Provider value={live(status)}>
        <Dashboard />
      </LiveDataContext.Provider>
    </MemoryRouter>
  );
}

function pipeline() {
  const title = screen.getByText(translate("en", "dash.pipeline"));
  const card = title.closest("[data-slot='card'], .rounded-xl, .rounded-lg") as HTMLElement;
  return within(card ?? document.body);
}

describe("Dashboard: the Pipeline card follows the programme selected in the header", () => {
  it("keeps programme 1's renditions, subscribers and meters when programme 2's frame arrives last", async () => {
    const { rerender } = render(view(frameOne));
    expect(await pipeline().findByText("1080p — primary")).toBeTruthy();
    expect(pipeline().getByText("9")).toBeTruthy();

    rerender(view(frameTwo));

    expect(
      pipeline().getByText("1080p — primary"),
      "the Pipeline card showed the other programme's rendition ladder",
    ).toBeTruthy();
    expect(pipeline().queryByText(translate("en", "i18nSweep.noRenditions"))).toBeNull();
    expect(pipeline().getByText("9"), "the relay subscriber count is the other programme's").toBeTruthy();
    expect(pipeline().queryByText("5")).toBeNull();
  });
});
