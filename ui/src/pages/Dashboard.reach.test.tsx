// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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

describe("Dashboard: the Ingest card while the backup is on air", () => {
  it("does not say plain Stopped over a live backup, and names the backup", async () => {
    draw();

    await screen.findByRole("button", { name: translate("en", "dash.failoverReturn") });
    expect(
      screen.getByText(translate("en", "dash.ingestBackupOnAir")),
      "the card reads STOPPED beside an on-air backup panel",
    ).toBeTruthy();
    // Stopped is the word an absent ingest now reads (A91); Offline is gone.
    expect(screen.queryByText("Stopped")).toBeNull();
    expect(screen.queryByText("Offline")).toBeNull();
  });

  it("keeps saying Stopped when the primary is on air and nothing arrives", async () => {
    draw(live({ status: status({ failover: { ...status().failover!, active: "primary" } }) }));
    await waitFor(() => expect(screen.getByText("Destinations")).toBeTruthy());
    expect(screen.queryByText(translate("en", "dash.ingestBackupOnAir"))).toBeNull();
    // Stopped is the console's one word for an absent process (A91).
    expect(screen.getAllByText("Stopped").length).toBeGreaterThan(0);
  });

  it("still names the backup when the primary has recovered but the return is manual", async () => {
    draw(
      live({
        status: status({ failover: { ...status().failover!, primaryLive: true } }),
        source: { id: 1, name: "Main", probed: true, tracks: [] },
        bitrate: [{ kbps: 4000 }, { kbps: 4000 }] as never,
      }),
    );
    expect(await screen.findByText(translate("en", "dash.ingestBackupOnAir"))).toBeTruthy();
  });
});

describe("Dashboard: the first-run checklist replaces the empty-destinations sentence", () => {
  it("draws the checklist when a source exists and nothing is configured to send it", async () => {
    draw();

    expect(
      await screen.findByText(translate("en", "firstRun.title")),
      "an operator with a source and no destination gets one sentence and no next step",
    ).toBeTruthy();
    expect(screen.queryByText(translate("en", "dash.noDestinations"))).toBeNull();
  });
});

describe("Dashboard: a fresh install starts the checklist at step one", () => {
  it("shows the checklist on an install with no source yet", async () => {
    const before = routes["/api/v1/setup"];
    routes["/api/v1/setup"] = { needsSetup: false, minPasswordChars: 8, sources: 0 };
    try {
      draw();
      expect(
        await screen.findByText(translate("en", "firstRun.title")),
        "a fresh install gets the explanation and nothing that says what to do next",
      ).toBeTruthy();
      // Two links to Sources on this screen: the explanation card's, and the
      // checklist's first step. The checklist's is the one under test.
      const checklist = screen.getByTestId("setup-checklist");
      expect(
        within(checklist).getByRole("link", { name: translate("en", "empty.goToSources") }),
      ).toBeTruthy();
    } finally {
      routes["/api/v1/setup"] = before;
    }
  });
});

describe("Dashboard: bulk start goes through the Go live sheet, bulk stop stays confirmed", () => {
  const oneDest = {
    id: 1,
    name: "Twitch — no music",
    kind: "rtmp",
    platform: "twitch",
    enabled: false,
    summary: "",
    tracks: null,
    filterComplex: "",
    normalization: "off",
    warnings: null,
    sourceId: 1,
  };

  it("keeps Go live the only filled start action, so Add destination is an outline (A9)", async () => {
    draw(live({ status: status({ destinations: [oneDest as never] }) }));
    const add = await screen.findByRole("button", { name: translate("en", "firstRun.addDestination") });
    expect(add.className).not.toContain("bg-primary");
    expect(add.className).toContain("border");
  });

  it("offers Go live instead of Start all, and no start fires from the bar itself", async () => {
    draw(live({ status: status({ destinations: [oneDest as never] }) }));

    expect(
      await screen.findByRole("button", { name: translate("en", "golive.trigger") }),
    ).toBeTruthy();
    // The bulk Start all button was removed (A5); its copy key is gone too, so
    // the assertion names the words directly rather than a key that no longer exists.
    expect(screen.queryByRole("button", { name: "Start all" })).toBeNull();
  });

  it("keeps Stop all behind its confirmation", async () => {
    draw(live({ status: status({ destinations: [{ ...oneDest, enabled: true } as never] }) }));

    // Two buttons carry this name: the toolbar's and the strip's (A4). Both open
    // the same confirmation, so either one serves; the toolbar's is outside the strip.
    const named = await screen.findAllByRole("button", { name: translate("en", "dash.stopAll") });
    fireEvent.click(named.find((b) => !b.closest('[data-testid="on-air-bar"]')) as HTMLElement);
    expect(await screen.findByRole("dialog")).toBeTruthy();
  });
});
