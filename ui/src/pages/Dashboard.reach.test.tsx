// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { Dashboard } from "./Dashboard";
import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import { translate } from "@/lib/i18n";
import type { Status } from "@/lib/types";

/* THE HALF A COMPONENT TEST CANNOT SEE.
 *
 * Both defects fixed here were of the same shape: the capability existed and
 * nothing on a screen reached it. #768's switch route had no caller; #786's
 * `descriptionMax` was declared in this file and read by nothing. A test that
 * renders the pieces in isolation proves the pieces work and would have passed
 * against the broken build too, because the broken build's pieces were fine --
 * they were simply not on the page.
 *
 * So this renders the whole Dashboard, over a stubbed server, and asks what an
 * operator would see. It is the only assertion in the suite that fails if the
 * control is written and never mounted. */

const originalFetch = globalThis.fetch;

/** One programme's live snapshot, with the failover tier on the backup feed. */
function status(over: Partial<Status> = {}): Status {
  return {
    renditions: [],
    destinations: [],
    relay: { port: 0, subscribers: [], rxPackets: 0, rxBytes: 0, txPackets: 0, dropped: 0, tsPackets: 0, tsLost: 0, discontinuities: 0, lossPercent: 0 },
    source: { id: 1, name: "Main", probed: true, tracks: [] },
    failover: {
      active: "backup",
      reason: "the primary ingest stopped delivering",
      switchedAt: "2026-09-09T10:00:00Z",
      switches: 1,
      primaryLive: true,
      backupLive: true,
      backupEnabled: true,
      slateEnabled: true,
    },
    ...over,
  } as Status;
}

function live(over: Partial<LiveData> = {}): LiveData {
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
    ...over,
  } as unknown as LiveData;
}

/** The settings this page reads on mount. `return: "manual"` is the server's
 *  own default and the whole premise of #768. */
const SETTINGS = {
  preview: { enabled: false },
  recording: { enabled: false },
  meters: { enabled: false },
  failover: { enabled: true, return: "manual" },
};

/** One YouTube target: the only platform that publishes a description limit,
 *  which is why the counter has something to show. */
const METADATA = {
  targets: [
    {
      accountId: 1,
      platform: "youtube",
      accountName: "Studio",
      caps: { fields: ["title", "description"], titleMax: 100, descriptionMax: 5000 },
    },
  ],
};

const routes: Record<string, unknown> = {
  "/api/v1/settings": SETTINGS,
  "/api/v1/system": { ingestUrl: "rtmp://box/live", ffmpeg: { hasLibsrt: true } },
  "/api/v1/setup": { needsSetup: false, minPasswordChars: 8, sources: 1 },
  "/api/v1/sources": [{ id: 1, name: "Main" }],
  "/api/v1/metadata": METADATA,
  "/api/v1/metadata/broadcast-window": { accounts: [] },
};

beforeEach(() => {
  // jsdom implements no scrolling at all, and the chat panel pins itself to the
  // bottom on every message. A gap in the environment rather than anything
  // about the page, so it is filled rather than worked around.
  if (!Element.prototype.scrollTo) {
    Element.prototype.scrollTo = () => {};
  }
  // Everything this page reads, answered from one table. Anything not named
  // answers `{}`, which every consumer here treats as "nothing to show" --
  // the shape a fresh install produces.
  globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(typeof input === "string" ? input : input.toString());
    const path = url.split("?")[0];
    return {
      ok: true,
      status: 200,
      text: async () => JSON.stringify(routes[path] ?? {}),
    } as unknown as Response;
  }) as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = originalFetch;
  vi.restoreAllMocks();
  cleanup();
});

function draw(value: LiveData = live()) {
  return render(
    <MemoryRouter>
      <LiveDataContext.Provider value={value}>
        <Dashboard />
      </LiveDataContext.Provider>
    </MemoryRouter>,
  );
}

describe("Dashboard: the failover control is actually on the page (#768)", () => {
  it("offers the way back to the primary while the backup is on air", async () => {
    draw();

    expect(
      await screen.findByRole("button", { name: translate("en", "dash.failoverReturn") }),
      "the dashboard renders no way back from a failover, which is the whole defect",
    ).toBeTruthy();
  });

  it("says nothing about failover while the primary is on air", async () => {
    draw(live({ status: status({ failover: { ...status().failover!, active: "primary" } }) }));

    // Waited for rather than asserted immediately: the settings read that feeds
    // the panel lands after the first paint, so an instant assertion would pass
    // against a build that draws the panel a tick later.
    await waitFor(() => expect(screen.getByText("Destinations")).toBeTruthy());
    expect(
      screen.queryByRole("button", { name: translate("en", "dash.failoverReturn") }),
      "a standing failover panel on a healthy install is the noise that hides the real one",
    ).toBeNull();
  });

  it("ignores a snapshot belonging to a different programme", async () => {
    // The status socket is install-wide: every engine publishes onto it and the
    // app keeps one snapshot. Acting on another programme's tier would put a
    // different show on its slate while reporting success for this one (#497).
    draw(live({ status: status({ source: { id: 2, name: "Studio B", probed: true, tracks: [] } }) }));

    await waitFor(() => expect(screen.getByText("Destinations")).toBeTruthy());
    expect(
      screen.queryByRole("button", { name: translate("en", "dash.failoverReturn") }),
    ).toBeNull();
  });
});

describe("Dashboard: the description counter (#786)", () => {
  it("counts the description down against the platform limit", async () => {
    draw();

    // The title counter has always been here; the description one was a dead
    // local beside it while the server enforced the limit and only said so
    // after the operator pressed Push.
    expect(await screen.findByText("0/5000")).toBeTruthy();
    expect(screen.getByText("0/100"), "the title counter must keep working").toBeTruthy();
  });

  it("shows no description counter for a platform that publishes no limit", async () => {
    routes["/api/v1/metadata"] = {
      targets: [
        {
          accountId: 2,
          platform: "twitch",
          accountName: "Studio",
          caps: { fields: ["title"], titleMax: 140 },
        },
      ],
    };
    draw();

    // Zero means "no published limit", not "zero characters". Rendering
    // "0/0" beside a field would be a limit nobody set.
    expect(await screen.findByText("0/140")).toBeTruthy();
    expect(screen.queryByText("0/0")).toBeNull();
    routes["/api/v1/metadata"] = METADATA;
  });
});
