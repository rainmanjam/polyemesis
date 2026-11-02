// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { AudiencePanel } from "./AudiencePanel";
import { StreamReports } from "./StreamReports";
import { api } from "@/lib/api";
import { translate } from "@/lib/i18n";
import type { AnalyticsLive, AnalyticsReport } from "@/lib/types";

const NOT_REPORTED = translate("en", "aud.notReported");

function live(over: Partial<AnalyticsLive> = {}): AnalyticsLive {
  return {
    enabled: true,
    onAir: true,
    stream: { id: 1, startedAt: "2026-11-01T10:00:00Z", durationMs: 60_000 },
    at: "2026-11-01T10:01:00Z",
    total: 15,
    reported: 2,
    readings: [
      { platform: "youtube", name: "Main", live: true, reason: "hidden" },
      { platform: "twitch", name: "Alt", live: true, viewers: 12 },
      { platform: "player", live: true, viewers: 3 },
    ],
    points: [{ at: "a", total: 10 }, { at: "b" }, { at: "c", total: 15 }],
    intervalSeconds: 60,
    ...over,
  };
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("AudiencePanel", () => {
  it("shows a hidden count as not reported and never as 0", async () => {
    vi.spyOn(api.analytics, "live").mockResolvedValue(live());
    render(<AudiencePanel />);
    await waitFor(() => expect(screen.getByTestId("audience-total").textContent).toBe("15"));
    const yt = screen.getByTestId("audience-row-youtube");
    expect(within(yt).getByText(NOT_REPORTED)).toBeTruthy();
    expect(yt.textContent).not.toMatch(/\b0\b/);
    expect(within(screen.getByTestId("audience-row-twitch")).getByText("12")).toBeTruthy();
    // The player is its own row, labelled, and last.
    const rows = screen.getAllByTestId(/^audience-row-/).map((r) => r.dataset.testid);
    expect(rows).toEqual(["audience-row-twitch", "audience-row-youtube", "audience-row-player"]);
    expect(screen.getByText(translate("en", "aud.player"))).toBeTruthy();
    expect(screen.getByText(translate("en", "aud.reportedOf", { n: 2, of: 3 }))).toBeTruthy();
    expect(screen.getByRole("img", { name: translate("en", "aud.sparkline") })).toBeTruthy();
  });

  it("says not reported for the total when nothing reported", async () => {
    vi.spyOn(api.analytics, "live").mockResolvedValue(
      live({ total: undefined, reported: 0, readings: [{ platform: "kick", live: true, reason: "hidden" }], points: [{ at: "a" }] }),
    );
    render(<AudiencePanel />);
    await waitFor(() => expect(screen.getByTestId("audience-total").textContent).toBe(NOT_REPORTED));
    // Nothing ever reported: no line to draw, and certainly not a flat 0.
    expect(screen.queryByRole("img", { name: translate("en", "aud.sparkline") })).toBeNull();
  });

  it("explains off air and disabled instead of showing numbers", async () => {
    const spy = vi.spyOn(api.analytics, "live").mockResolvedValue(live({ onAir: false, readings: [], total: undefined }));
    render(<AudiencePanel />);
    await waitFor(() => expect(screen.getByText(translate("en", "aud.offAir"))).toBeTruthy());
    expect(screen.queryByTestId("audience-total")).toBeNull();
    cleanup();
    spy.mockResolvedValue(live({ enabled: false, onAir: false }));
    render(<AudiencePanel />);
    await waitFor(() => expect(screen.getByText(translate("en", "aud.disabled"))).toBeTruthy());
  });

  it("labels the total as including the public player, and says when a tick is partial or stretched", async () => {
    vi.spyOn(api.analytics, "live").mockResolvedValue(
      live({ partial: true, stretched: true, effectiveIntervalSeconds: 240 }),
    );
    render(<AudiencePanel />);
    await waitFor(() => expect(screen.getByText(translate("en", "aud.totalWithPlayer"))).toBeTruthy());
    expect(screen.getByTestId("audience-partial").textContent).toBe(translate("en", "aud.partial"));
    expect(screen.getByTestId("audience-stretched").textContent).toBe(translate("en", "aud.stretched", { seconds: 240 }));
  });

  it("shows a pasted-key destination as not connected rather than leaving it out", async () => {
    vi.spyOn(api.analytics, "live").mockResolvedValue(
      live({ readings: [{ platform: "kick", name: "Kick by key", live: false, reason: "unlinked" }], total: undefined, reported: 0 }),
    );
    render(<AudiencePanel />);
    const row = await screen.findByTestId("audience-row-kick");
    expect(within(row).getByText(translate("en", "aud.notConnected"))).toBeTruthy();
    expect(screen.queryByText(translate("en", "aud.totalWithPlayer"))).toBeNull();
  });

  it("survives a refused read", async () => {
    vi.spyOn(api.analytics, "live").mockRejectedValue(new Error("403"));
    render(<AudiencePanel />);
    await waitFor(() => expect(screen.getByText(translate("en", "aud.unavailable"))).toBeTruthy());
  });
});

function report(): AnalyticsReport {
  return {
    stream: { id: 7, title: "Friday", startedAt: "2026-11-01T10:00:00Z", endedAt: "2026-11-01T11:00:00Z", durationMs: 3_600_000, peakTotal: 30 },
    summary: {
      rows: [
        { platform: "kick", name: "K", samples: 3, reported: 0 },
        { platform: "twitch", name: "T", samples: 3, reported: 3, peak: 30, average: 20 },
      ],
      peakTotal: 30,
      averageTotal: 20,
      points: [],
    },
    chat: [{ platform: "twitch", messages: 4 }],
    topChatters: [{ platform: "twitch", name: "regular", messages: 3 }],
    recordings: [{ id: 5, filename: "take.mkv", startedAt: "2026-11-01T10:00:00Z", durationMs: 1000, transcript: true }],
    clips: [{ name: "clip.ts", createdAt: "2026-11-01T10:30:00Z", seconds: 30 }],
  };
}

describe("StreamReports", () => {
  it("lists streams and opens a report with not-reported rows, chat, links and CSV", async () => {
    vi.spyOn(api.analytics, "streams").mockResolvedValue({ streams: [report().stream] });
    vi.spyOn(api.analytics, "report").mockResolvedValue(report());
    render(
      <MemoryRouter>
        <StreamReports />
      </MemoryRouter>,
    );
    const open = await screen.findByRole("button", { name: translate("en", "aud.showReport") });
    open.click();
    const rep = await screen.findByTestId("stream-report");
    const kick = within(rep).getByTestId("report-row-kick");
    expect(within(kick).getAllByText(NOT_REPORTED)).toHaveLength(2);
    expect(within(rep).getByTestId("report-row-twitch").textContent).toContain("30");
    expect(within(rep).getByText("regular")).toBeTruthy();
    expect(within(rep).getByText(translate("en", "aud.downloadCsv")).getAttribute("href")).toBe(
      "/api/v1/analytics/streams/7?format=csv",
    );
    expect(within(rep).getByText("take.mkv").getAttribute("href")).toBe("/api/v1/recordings/5/download");
    expect(within(rep).getByText(translate("en", "aud.transcript"))).toBeTruthy();
    expect(within(rep).getByText("clip.ts").getAttribute("href")).toBe("/api/v1/clips/clip.ts/download");
  });

  it("says so when no stream has been recorded", async () => {
    vi.spyOn(api.analytics, "streams").mockResolvedValue({ streams: [] });
    render(
      <MemoryRouter>
        <StreamReports />
      </MemoryRouter>,
    );
    await waitFor(() => expect(screen.getByText(translate("en", "aud.noStreams"))).toBeTruthy());
  });
});
