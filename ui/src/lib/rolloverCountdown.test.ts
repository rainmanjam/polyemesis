import { describe, expect, it } from "vitest";
import { countdownFor, formatRemaining } from "./rolloverCountdown";
import type { RolloverView } from "./types";

// A view is the server's RolloverView. Only the fields the countdown reads are set here.
function view(over: Partial<RolloverView> = {}): RolloverView {
  return {
    settings: { destinationId: 1, enabled: true, marginMinutes: 30, chatNotice: false },
    platform: "twitch",
    action: "countdown",
    limit: {
      presetId: "twitch",
      kind: "broadcastDuration",
      hours: 48,
      status: "reported",
      source: "https://help.twitch.tv/s/article/broadcasting-guidelines?language=en_US",
    },
    delivering: true,
    due: false,
    ...over,
  };
}

const now = new Date("2026-10-31T12:00:00Z");

describe("countdownFor", () => {
  it("shows no countdown when the platform has no cap at all", () => {
    expect(countdownFor(view({ limit: undefined }), now)).toEqual({ kind: "none", reason: "no-limit" });
  });

  it("shows no countdown for an unverified cap, even one that carries hours", () => {
    const v = view({ limit: { presetId: "kick", kind: "broadcastDuration", hours: 48, status: "unverified" } });
    expect(countdownFor(v, now)).toEqual({ kind: "none", reason: "unverified" });
  });

  it("shows no countdown for a zero-hour cap, because the number is not known", () => {
    const v = view({ limit: { presetId: "kick", kind: "broadcastDuration", hours: 0, status: "documented" } });
    expect(countdownFor(v, now)).toEqual({ kind: "none", reason: "unverified" });
  });

  it("shows no countdown while the destination is not delivering, because nothing is counting", () => {
    expect(countdownFor(view({ delivering: false }), now)).toEqual({ kind: "none", reason: "not-delivering" });
  });

  it("waits, rather than guessing, while delivering with no cutoff recorded yet", () => {
    expect(countdownFor(view({ cutoffAt: undefined }), now)).toEqual({ kind: "waiting" });
  });

  it("counts down to the cutoff and marks a reported cap as reported", () => {
    const state = countdownFor(view({ cutoffAt: "2026-11-02T12:00:00Z" }), now);
    expect(state.kind).toBe("running");
    if (state.kind !== "running") return;
    expect(state.remainingMs).toBe(48 * 3_600_000);
    expect(state.reported).toBe(true);
  });

  it("does not mark a documented cap as reported", () => {
    const v = view({
      cutoffAt: "2026-11-01T12:00:00Z",
      limit: { presetId: "youtube", kind: "archiveDuration", hours: 12, status: "documented" },
    });
    const state = countdownFor(v, now);
    expect(state.kind === "running" && state.reported).toBe(false);
  });

  it("never reports negative time once the cutoff has passed", () => {
    const state = countdownFor(view({ cutoffAt: "2026-10-30T12:00:00Z" }), now);
    expect(state.kind === "running" && state.remainingMs).toBe(0);
  });
});

describe("formatRemaining", () => {
  it("shows hours and zero-padded minutes once an hour or more remains", () => {
    expect(formatRemaining(3 * 3_600_000 + 5 * 60_000)).toBe("3h 05m");
  });
  it("shows minutes alone under an hour", () => {
    expect(formatRemaining(42 * 60_000)).toBe("42m");
  });
  it("clamps a negative duration to zero", () => {
    expect(formatRemaining(-5_000)).toBe("0m");
  });
});
