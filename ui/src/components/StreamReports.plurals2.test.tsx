// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { StreamReports } from "./StreamReports";
import { api } from "@/lib/api";
import { setLanguage } from "@/lib/i18n";
import type { AnalyticsReport } from "@/lib/types";

/* The public player's QoE line in a past stream's report. Three counts, so
 * "label: value" pairs rather than a One key per count: the old sentence read
 * "1 sessions, 1 rebuffers, 1 sessions with errors". */

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  setLanguage("en");
});

function report(n: number): AnalyticsReport {
  return {
    stream: { id: 7, title: "Friday", startedAt: "2026-11-01T10:00:00Z", endedAt: "2026-11-01T11:00:00Z", durationMs: 3_600_000, peakTotal: 30 },
    summary: { rows: [], peakTotal: 30, averageTotal: 20, points: [] },
    chat: [],
    topChatters: [],
    recordings: [],
    clips: [],
    player: { sessions: n, startups: n, startupMs: 0, rebuffers: n, rebufferMs: 0, sessionsWithErrors: n },
  };
}

async function openReport(n: number): Promise<HTMLElement> {
  vi.spyOn(api.analytics, "streams").mockResolvedValue({ streams: [report(n).stream] });
  vi.spyOn(api.analytics, "report").mockResolvedValue(report(n));
  render(
    <MemoryRouter>
      <StreamReports />
    </MemoryRouter>,
  );
  (await screen.findByRole("button", { name: "Report" })).click();
  return screen.findByTestId("stream-report");
}

describe("StreamReports: aud.playerQoE (en)", () => {
  it("reads sessions: 1 · rebuffers: 1 · sessions with errors: 1, not 1 sessions", async () => {
    const rep = await openReport(1);
    expect(
      within(rep).getByText("Public player — sessions: 1 · rebuffers: 1 · sessions with errors: 1"),
    ).toBeTruthy();
    expect(within(rep).queryByText("Public player: 1 sessions, 1 rebuffers, 1 sessions with errors")).toBeNull();
  });

  it("reads sessions: 2 · rebuffers: 2 · sessions with errors: 2", async () => {
    const rep = await openReport(2);
    expect(
      within(rep).getByText("Public player — sessions: 2 · rebuffers: 2 · sessions with errors: 2"),
    ).toBeTruthy();
  });
});
