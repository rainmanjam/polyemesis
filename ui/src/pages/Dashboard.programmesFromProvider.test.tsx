// @vitest-environment jsdom

/* THE DASHBOARD HAS ONE LIST OF PROGRAMMES, AND IT IS THE PROVIDER'S.
 *
 * The page used to keep its own one-shot /sources read, re-run only when a
 * destination changed, beside LiveDataProvider's 10 s poll of the same route.
 * Add, rename or delete a programme elsewhere and the lanes, the Go live rows
 * and the checklist's publish address kept the old answer until the page
 * remounted. These render the page, change what the provider says, and read
 * what an operator sees. */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("@/components/DestinationDialog", () => ({ DestinationDialog: () => null }));

import { Dashboard } from "./Dashboard";
import { LiveDataContext, type LiveData, type Programme } from "@/hooks/useLiveData";
import { translate } from "@/lib/i18n";
import type { Status } from "@/lib/types";

const originalFetch = globalThis.fetch;
let requested: string[] = [];

const relay = { port: 0, subscribers: [], rxPackets: 0, rxBytes: 0, txPackets: 0, dropped: 0, tsPackets: 0, tsLost: 0, discontinuities: 0, lossPercent: 0 };

const dest = (id: number, sourceId: number) => ({
  id, name: `Dest ${id}`, kind: "rtmp", platform: "twitch", enabled: false, summary: "", tracks: null,
  filterComplex: "", normalization: "off", warnings: null, sourceId,
});

const prog = (id: number, name: string, publishUrls: Record<string, string> = {}): Programme => ({ id, name, publishUrls });

function live(programmes: Programme[], destinations: unknown[]): LiveData {
  const status = { renditions: [], destinations, relay, source: { id: 1, name: "A", probed: true, tracks: [] } } as unknown as Status;
  return {
    programme: 1, programmeKnown: true, programmes, sourceCount: programmes.length, sourceCountKnown: true,
    connected: true, snapshotKnown: true, status, source: status.source, levels: null, system: null, bitrate: [], logs: [],
  } as unknown as LiveData;
}

const tree = (value: LiveData) => (
  <MemoryRouter>
    <LiveDataContext.Provider value={value}>
      <Dashboard />
    </LiveDataContext.Provider>
  </MemoryRouter>
);

beforeEach(() => {
  requested = [];
  if (!Element.prototype.scrollTo) Element.prototype.scrollTo = () => {};
  globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
    const path = String(typeof input === "string" ? input : input.toString()).split("?")[0];
    requested.push(path);
    const routes: Record<string, unknown> = {
      "/api/v1/settings": { preview: { enabled: false }, recording: { enabled: false }, meters: { enabled: false }, failover: { enabled: true } },
      "/api/v1/system": { ingestUrl: "srt://box:6000", ffmpeg: { hasLibsrt: true } },
      "/api/v1/metadata": { targets: [] },
      "/api/v1/metadata/broadcast-window": { accounts: [] },
    };
    return { ok: true, status: 200, text: async () => JSON.stringify(routes[path] ?? {}) } as unknown as Response;
  }) as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = originalFetch;
  vi.restoreAllMocks();
  cleanup();
});

describe("Dashboard reads the programmes from LiveDataProvider", () => {
  it("never asks /sources itself", async () => {
    render(tree(live([prog(1, "Studio A"), prog(2, "Studio B")], [dest(1, 1), dest(2, 2)])));
    await screen.findByText("Studio A", { selector: "[data-lane-name]" });
    expect(requested).not.toContain("/api/v1/sources");
  });

  it("redraws its lanes when a programme is renamed and another is added", async () => {
    const dests = [dest(1, 1), dest(2, 2), dest(3, 3)];
    const view = render(tree(live([prog(1, "Studio A"), prog(2, "Studio B")], dests)));
    await screen.findByText("Studio B", { selector: "[data-lane-name]" });
    expect(screen.queryByText("Studio C", { selector: "[data-lane-name]" })).toBeNull();

    view.rerender(tree(live([prog(1, "Studio A"), prog(2, "Backstage"), prog(3, "Studio C")], dests)));
    expect(await screen.findByText("Backstage", { selector: "[data-lane-name]" })).toBeTruthy();
    expect(screen.getByText("Studio C", { selector: "[data-lane-name]" })).toBeTruthy();
    expect(screen.queryByText("Studio B", { selector: "[data-lane-name]" })).toBeNull();
  });

  it("unblocks a Go live row when the programme its destination carries appears", async () => {
    const dests = [dest(1, 1), dest(2, 3)];
    const view = render(tree(live([prog(1, "Studio A"), prog(2, "Studio B")], dests)));
    fireEvent.click(await screen.findByRole("button", { name: translate("en", "golive.trigger") }));
    expect(await screen.findByText(translate("en", "golive.blockedNoSource"))).toBeTruthy();
    fireEvent.keyDown(document.body, { key: "Escape" });

    view.rerender(tree(live([prog(1, "Studio A"), prog(2, "Studio B"), prog(3, "Studio C")], dests)));
    // Closed and reopened: the rows are decided when the button is pressed.
    const button = await screen.findByRole("button", { name: translate("en", "golive.trigger") });
    fireEvent.click(button);
    expect(await screen.findByTitle("Studio C")).toBeTruthy();
    expect(screen.queryByText(translate("en", "golive.blockedNoSource"))).toBeNull();
  });

  it("offers the checklist's publish address from the provider's current programme", async () => {
    const written: string[] = [];
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: async (v: string) => void written.push(v) },
    });
    const copyAddress = async () => {
      fireEvent.click(await screen.findByRole("button", { name: /Copy SRT address/ }));
      await waitFor(() => expect(written.length).toBeGreaterThan(0));
      return written[written.length - 1];
    };

    const view = render(tree(live([prog(1, "Main", { srt: "srt://<server>:6000" })], [])));
    expect(await copyAddress()).toContain(":6000");

    view.rerender(tree(live([prog(1, "Main", { srt: "srt://<server>:7777" })], [])));
    written.length = 0;
    expect(await copyAddress()).toContain(":7777");
  });
});
