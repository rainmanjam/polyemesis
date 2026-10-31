// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { Dashboard } from "./Dashboard";
import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import { NARROW_QUERY } from "@/hooks/useIsNarrow";
import { translate } from "@/lib/i18n";
import type { Status } from "@/lib/types";

/* THE DASHBOARD ON A PHONE (app-council A87).
 *
 * At 390px the page was about 6,000px tall, and the destination list -- the
 * thing an operator opens the dashboard on a phone to check -- started about
 * 2,000px down, under Chat, the Pipeline card and the stream-info composer.
 * Below the lg breakpoint the order is now: what is on air, then the
 * destinations with Go live, then the Ingest card, then Chat and Pipeline and
 * Stream info as closed disclosures.
 *
 * The order is the DOM order, not a CSS `order`, so a screen reader and the
 * Tab key meet the sections in the order they are drawn. That is also what
 * makes it testable here: jsdom loads no stylesheet, but it does know which
 * node comes first.
 *
 * jsdom has no matchMedia. With none, the page is the desktop page, which is
 * what every other Dashboard test renders; the narrow cases below install a
 * stub that answers NARROW_QUERY only. */

const originalFetch = globalThis.fetch;
const originalMatchMedia = window.matchMedia;

const relay = { port: 0, subscribers: [], rxPackets: 0, rxBytes: 0, txPackets: 0, dropped: 0, tsPackets: 0, tsLost: 0, discontinuities: 0, lossPercent: 0 };

/** One enabled, running destination: enough for the list to draw a card and
 *  for the failover-exposure line to have something to protect. */
const DEST = {
  id: 1,
  name: "Dest 1",
  kind: "rtmp",
  platform: "twitch",
  enabled: true,
  summary: "",
  tracks: null,
  filterComplex: "",
  normalization: "off",
  warnings: null,
  sourceId: 1,
  process: { name: "d1", kind: "destination", state: "running", pid: 1, restarts: 0, uptimeSec: 30 },
};

function status(): Status {
  return {
    renditions: [],
    destinations: [DEST],
    relay,
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

/** Failover off, so the exposure line renders: its place moves too. */
const routes: Record<string, unknown> = {
  "/api/v1/settings": {
    preview: { enabled: false },
    recording: { enabled: false },
    meters: { enabled: false },
    failover: { enabled: false },
  },
  "/api/v1/system": { ingestUrl: "rtmp://box/live", ffmpeg: { hasLibsrt: true } },
  "/api/v1/setup": { needsSetup: false, minPasswordChars: 8, sources: 1 },
  "/api/v1/sources": [{ id: 1, name: "Main" }],
  "/api/v1/metadata": { targets: [], last: null },
  "/api/v1/metadata/broadcast-window": { accounts: [] },
};

function stubMatchMedia(narrow: boolean) {
  window.matchMedia = ((query: string) => ({
    matches: narrow && query === NARROW_QUERY,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
}

beforeEach(() => {
  if (!Element.prototype.scrollTo) Element.prototype.scrollTo = () => {};
  globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input).split("?")[0];
    return {
      ok: true,
      status: 200,
      text: async () => JSON.stringify(routes[path] ?? {}),
    } as unknown as Response;
  }) as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = originalFetch;
  window.matchMedia = originalMatchMedia;
  vi.restoreAllMocks();
  cleanup();
});

function draw() {
  return render(
    <MemoryRouter>
      <LiveDataContext.Provider value={live()}>
        <Dashboard />
      </LiveDataContext.Provider>
    </MemoryRouter>,
  );
}

/** True when `a` comes before `b` in the document. */
const before = (a: Node, b: Node) => Boolean(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING);

const chatPipelineLabel = translate("en", "dashMobile.chatPipeline");
const streamInfoLabel = translate("en", "dashMobile.streamInfo");

describe("Dashboard below the lg breakpoint (A87)", () => {
  it("puts the destinations, with Go live, before the Ingest card, Chat, Pipeline and Stream info", async () => {
    stubMatchMedia(true);
    draw();

    const heading = await screen.findByText("Destinations");
    const goLive = screen.getByRole("button", { name: translate("en", "golive.trigger") });
    const card = await screen.findByText("Dest 1");
    const ingest = screen.getByText(translate("en", "chrome.ingest"));
    const chatToggle = screen.getByRole("button", { name: chatPipelineLabel });
    const infoToggle = screen.getByRole("button", { name: streamInfoLabel });

    expect(before(heading, goLive), "Go live sits with the destinations it starts").toBe(true);
    expect(before(card, ingest), "the destination list must come before the Ingest card").toBe(true);
    expect(before(ingest, chatToggle), "Chat and Pipeline follow the Ingest card").toBe(true);
    expect(before(card, chatToggle), "a phone again opens on Chat above the destinations").toBe(true);
    expect(before(chatToggle, infoToggle)).toBe(true);
  });

  it("moves the failover-exposure line below the destination list", async () => {
    stubMatchMedia(true);
    draw();

    const lead = await screen.findByText(translate("en", "i18nSweep.noFailoverLead"));
    const card = await screen.findByText("Dest 1");
    expect(before(card, lead), "the standing failover line still pushes the list down").toBe(true);
  });

  it("starts Chat, Pipeline and Stream info collapsed, and opens them on a press", async () => {
    stubMatchMedia(true);
    draw();

    const chatToggle = await screen.findByRole("button", { name: chatPipelineLabel });
    const infoToggle = screen.getByRole("button", { name: streamInfoLabel });
    expect(chatToggle.getAttribute("aria-expanded")).toBe("false");
    expect(infoToggle.getAttribute("aria-expanded")).toBe("false");

    const pipeline = screen.getByText(translate("en", "dash.pipeline"));
    const composer = screen.getByTestId("golive-composer");
    expect(pipeline.closest("[hidden]"), "the Pipeline card is open on a phone").not.toBeNull();
    expect(composer.closest("[hidden]"), "the stream-info composer is open on a phone").not.toBeNull();

    fireEvent.click(chatToggle);
    expect(chatToggle.getAttribute("aria-expanded")).toBe("true");
    expect(pipeline.closest("[hidden]")).toBeNull();
    expect(composer.closest("[hidden]"), "one toggle opened the other section").not.toBeNull();

    fireEvent.click(infoToggle);
    expect(composer.closest("[hidden]")).toBeNull();
  });

  it("keeps the Ingest card open on a phone (layout.spec measures it at 390px)", async () => {
    stubMatchMedia(true);
    draw();

    const ingest = await screen.findByText(translate("en", "chrome.ingest"));
    expect(ingest.closest("[hidden]")).toBeNull();
  });
});

describe("Dashboard at desktop widths: as it was", () => {
  for (const [name, setup] of [
    ["with no matchMedia (jsdom's default)", () => {}],
    ["with a matchMedia that reports a wide window", () => stubMatchMedia(false)],
  ] as const) {
    it(`draws no disclosures and keeps the top row above the destinations, ${name}`, async () => {
      setup();
      draw();

      const heading = await screen.findByText("Destinations");
      expect(screen.queryByRole("button", { name: chatPipelineLabel })).toBeNull();
      expect(screen.queryByRole("button", { name: streamInfoLabel })).toBeNull();

      const pipeline = screen.getByText(translate("en", "dash.pipeline"));
      const composer = screen.getByTestId("golive-composer");
      expect(pipeline.closest("[hidden]")).toBeNull();
      expect(composer.closest("[hidden]")).toBeNull();
      expect(before(pipeline, heading), "desktop keeps Chat and Pipeline above the destinations").toBe(true);
      expect(before(composer, heading), "desktop keeps Stream info above the destinations").toBe(true);

      // The exposure line stays between the heading and Go live, as before.
      const lead = await screen.findByText(translate("en", "i18nSweep.noFailoverLead"));
      const goLive = screen.getByRole("button", { name: translate("en", "golive.trigger") });
      expect(before(heading, lead)).toBe(true);
      expect(before(lead, goLive)).toBe(true);
    });
  }
});
