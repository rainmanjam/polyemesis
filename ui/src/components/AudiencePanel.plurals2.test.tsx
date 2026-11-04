// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { AudiencePanel } from "./AudiencePanel";
import { api } from "@/lib/api";
import { setLanguage } from "@/lib/i18n";
import type { AnalyticsLive, AnalyticsPlayerQoE } from "@/lib/types";

/* The public player's QoE line. It has three counts, so it is written as
 * "label: value" pairs rather than given a One key per count: the old
 * sentence read "1 sessions, 1 rebuffers, 1 sessions with errors". */

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  setLanguage("en");
});

const player = (n: number): AnalyticsPlayerQoE => ({
  sessions: n,
  startups: n,
  startupMs: 0,
  rebuffers: n,
  rebufferMs: 0,
  sessionsWithErrors: n,
});

function live(n: number): AnalyticsLive {
  return {
    enabled: true,
    onAir: true,
    stream: { id: 1, startedAt: "2026-11-01T10:00:00Z", durationMs: 60_000 },
    at: "2026-11-01T10:01:00Z",
    total: 3,
    reported: 1,
    readings: [{ platform: "player", live: true, viewers: 3 }],
    points: [{ at: "a", total: 3 }],
    intervalSeconds: 60,
    player: player(n),
  };
}

describe("AudiencePanel: aud.playerQoE (en)", () => {
  it("reads sessions: 1 · rebuffers: 1 · sessions with errors: 1, not 1 sessions", async () => {
    vi.spyOn(api.analytics, "live").mockResolvedValue(live(1));
    render(<AudiencePanel />);
    expect(
      await screen.findByText("Public player — sessions: 1 · rebuffers: 1 · sessions with errors: 1"),
    ).toBeTruthy();
    expect(screen.queryByText("Public player: 1 sessions, 1 rebuffers, 1 sessions with errors")).toBeNull();
  });

  it("reads sessions: 2 · rebuffers: 2 · sessions with errors: 2", async () => {
    vi.spyOn(api.analytics, "live").mockResolvedValue(live(2));
    render(<AudiencePanel />);
    expect(
      await screen.findByText("Public player — sessions: 2 · rebuffers: 2 · sessions with errors: 2"),
    ).toBeTruthy();
  });
});
