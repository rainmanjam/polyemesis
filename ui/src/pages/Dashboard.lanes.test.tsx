// @vitest-environment jsdom
//
// app-council A89. On a multi-programme install each programme gets a lane, and
// its heading read "MAIN PROGRAMME — STUDIO A 9": a bare number with no noun
// (destinations? live? sources?) and nothing but the cards' colours to say
// whether the lane was on air. The heading now says "9 destinations · 5 live"
// in words, and carries a cue that is not colour: a different glyph shape and
// a data-lane-state an assistive or test reader can query.
//
// This is the multi-programme harness the lane needs: two sources from
// /sources (so destinationLayout groups), and one install-wide status frame
// carrying destinations on both programmes.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

// The dialog stands in as the props it was opened with: which programme a new
// destination starts on is the claim, and the real dialog's own fetches are
// not this file's business.
vi.mock("@/components/DestinationDialog", () => ({
  DestinationDialog: (p: { open: boolean; initialSourceId?: string }) =>
    p.open ? <div role="dialog" data-initial-source={p.initialSourceId ?? ""} /> : null,
}));

import { Dashboard } from "./Dashboard";
import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import { translate } from "@/lib/i18n";
import type { Status } from "@/lib/types";

const originalFetch = globalThis.fetch;

const relay = { port: 0, subscribers: [], rxPackets: 0, rxBytes: 0, txPackets: 0, dropped: 0, tsPackets: 0, tsLost: 0, discontinuities: 0, lossPercent: 0 };

const dest = (id: number, sourceId: number, running: boolean) => ({
  id,
  name: `Dest ${id}`,
  kind: "rtmp",
  platform: "twitch",
  enabled: running,
  summary: "",
  tracks: null,
  filterComplex: "",
  normalization: "off",
  warnings: null,
  sourceId,
  ...(running ? { process: { name: `d${id}`, kind: "destination", state: "running", pid: id, restarts: 0, uptimeSec: 30 } } : {}),
});

/** Studio A: three destinations, two live. Studio B: one, switched off. */
const status = {
  renditions: [],
  destinations: [dest(1, 1, true), dest(2, 1, true), dest(3, 1, false), dest(4, 2, false)],
  relay,
  source: { id: 1, name: "Studio A", probed: true, tracks: [] },
} as unknown as Status;

const live: LiveData = {
  programme: 1,
  programmeKnown: true,
  programmes: [
    { id: 1, name: "Studio A" },
    { id: 2, name: "Studio B" },
  ],
  sourceCount: 2,
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

const draw = (over?: Partial<Status>) =>
  render(
    <MemoryRouter>
      <LiveDataContext.Provider value={over ? ({ ...live, status: { ...status, ...over } } as LiveData) : live}>
        <Dashboard />
      </LiveDataContext.Provider>
    </MemoryRouter>,
  );

async function laneHeading(name: string): Promise<HTMLElement> {
  const label = await screen.findByText(name, { selector: "[data-lane-name]" });
  return label.closest("h3") as HTMLElement;
}

describe("Dashboard lanes: headings say what the number counts and whether the lane is on air", () => {
  it("reads '3 destinations · 2 live' on the live lane, in words", async () => {
    draw();
    const a = await laneHeading("Studio A");
    expect(a.textContent).toContain(translate("en", "dash.laneCounts", { count: 3, live: 2 }));
    expect(a.textContent).toContain("3 destinations · 2 live");
  });

  it("marks on air and off air with a cue that is not colour", async () => {
    draw();
    const a = await laneHeading("Studio A");
    const b = await laneHeading("Studio B");
    expect(a.getAttribute("data-lane-state")).toBe("live");
    expect(b.getAttribute("data-lane-state")).toBe("idle");
    // One destination is singular, not "1 destinations".
    expect(b.textContent).toContain("1 destination · 0 live");
    // A different glyph, not the same glyph in another colour.
    const glyph = (h: HTMLElement) => h.querySelector("[data-lane-cue]")?.getAttribute("data-lane-cue");
    expect(glyph(a)).toBe("on-air");
    expect(glyph(b)).toBe("off-air");
  });
});

describe("Dashboard: empty lists use the kit's EmptyState (app-council A83)", () => {
  it("draws a programme with no destinations as an EmptyState inside its lane", async () => {
    draw({ destinations: [dest(1, 1, true)] } as Partial<Status>);
    const title = await screen.findByText(translate("en", "dash.destNoneHere"));
    expect(title.className).toContain("font-semibold");
    expect(title.closest(".border-dashed")).not.toBeNull();
  });

  // The empty lane carries the kit's primary action, and it adds on THAT
  // lane's programme: the round-2 review found "No destinations on this
  // programme yet." on Studio B with no way to add one from there.
  it("gives an empty lane an Add destination action for that lane's programme", async () => {
    draw({ destinations: [dest(1, 1, true)] } as Partial<Status>);
    const add = await screen.findByRole("button", {
      name: `${translate("en", "firstRun.addDestination")}: Studio B`,
    });
    fireEvent.click(add);
    const dialog = await screen.findByRole("dialog");
    // Studio B is source 2 in this harness.
    expect(dialog.getAttribute("data-initial-source")).toBe("2");
  });

  it("draws an install with no destinations as an EmptyState whose action opens Add destination", async () => {
    draw({ destinations: [] } as Partial<Status>);
    const title = await screen.findByText(translate("en", "dash.noDestinationsTitle"));
    const box = title.closest(".border-dashed") as HTMLElement;
    expect(box).not.toBeNull();
    expect(box.textContent).toContain(translate("en", "dash.noDestinationsBody"));
    expect(box.querySelector("button")?.textContent).toContain(translate("en", "firstRun.addDestination"));
  });
});
