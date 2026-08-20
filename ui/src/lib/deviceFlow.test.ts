import { describe, expect, it } from "vitest";

import {
  DEVICE_POLL_FLOOR_MS,
  deviceCountdown,
  deviceFlowAfterPoll,
  deviceFlowIsPolling,
  deviceHasExpired,
  devicePollDelayMs,
  deviceSecondsRemaining,
  type DeviceFlowPhase,
} from "./deviceFlow";
import type { DeviceAuth, DevicePoll, PlatformAccount } from "./types";

const auth: DeviceAuth = {
  handle: "h-1",
  userCode: "ABCD-1234",
  verificationUri: "https://www.twitch.tv/activate?public=true",
  expiresAt: "2026-08-19T12:30:00Z",
  intervalSeconds: 5,
};

const waiting: DeviceFlowPhase = { kind: "waiting", auth };

/* The interval is the property with a COST attached, so it gets the most
 * assertions: polling faster than the platform asked gets the operator's whole
 * developer app rate-limited, mid-connect, and their only symptom is that
 * connecting stopped working. */
describe("devicePollDelayMs", () => {
  it("honours a wider interval than the floor, because the platform may widen it", () => {
    expect(devicePollDelayMs(30)).toBe(30_000);
  });

  it("never returns less than the floor", () => {
    expect(devicePollDelayMs(1)).toBe(DEVICE_POLL_FLOOR_MS);
    expect(devicePollDelayMs(5)).toBe(DEVICE_POLL_FLOOR_MS);
  });

  // The mutation this exists for: `retryInSeconds ?? 0` is a setTimeout of zero
  // and a tab that polls as fast as the network allows. Every one of these
  // reads as "the server did not say", and only an actual finite number is an
  // answer -- the same reading viewerReadout takes of an absent viewerCount.
  it.each([
    ["zero", 0],
    ["negative", -10],
    ["undefined", undefined],
    ["null", null],
    ["NaN", Number.NaN],
    ["Infinity", Number.POSITIVE_INFINITY],
  ])("falls back to the floor when the server said %s", (_name, value) => {
    expect(devicePollDelayMs(value as number)).toBe(DEVICE_POLL_FLOOR_MS);
  });

  it("mirrors internal/oauth's own five-second floor", () => {
    // Not an arbitrary constant on either side: deviceMinPollInterval is 5s and
    // floors the platform's answer server-side, so a client floor that
    // disagreed would be the two halves promising different things.
    expect(DEVICE_POLL_FLOOR_MS).toBe(5_000);
  });
});

describe("deviceFlowAfterPoll", () => {
  it("stays waiting on pending, which is the answer to nearly every poll", () => {
    const next = deviceFlowAfterPoll(waiting, { state: "pending", retryInSeconds: 5 });
    expect(next).toBe(waiting);
    expect(deviceFlowIsPolling(next)).toBe(true);
  });

  it("connects with the account name the operator will recognise", () => {
    const account = { accountName: "Dallas", accountRef: "44322889" } as PlatformAccount;
    const next = deviceFlowAfterPoll(waiting, { state: "connected", account });
    expect(next).toEqual({ kind: "connected", accountName: "Dallas" });
    expect(deviceFlowIsPolling(next)).toBe(false);
  });

  it("still connects when the platform sent no display name", () => {
    // The account IS connected. Refusing to say so because a display name was
    // blank would leave the dialog polling a flow that already finished.
    const next = deviceFlowAfterPoll(waiting, { state: "connected" });
    expect(next).toEqual({ kind: "connected", accountName: "" });
  });

  it("stops on expired and carries the server's own sentence", () => {
    const next = deviceFlowAfterPoll(waiting, {
      state: "expired",
      reason: "this code has already been used or has expired; start again",
    });
    expect(next.kind).toBe("failed");
    expect(deviceFlowIsPolling(next)).toBe(false);
    if (next.kind === "failed") {
      expect(next.reason).toContain("already been used");
    }
  });

  // An expiry the server did not explain still has to STOP the flow. The empty
  // reason is not a placeholder to be shown: DeviceCodeDialog renders
  // `phase.reason || t("device.expired")`, so "" is what lets the dialog fall
  // back to its own sentence. A paraphrase invented here would win that `||`
  // and permanently replace a translated string with an English one.
  it("stops on an expiry the server sent no words for, leaving the reason empty", () => {
    const next = deviceFlowAfterPoll(waiting, { state: "expired" });
    expect(next).toEqual({ kind: "failed", reason: "" });
    expect(deviceFlowIsPolling(next)).toBe(false);
  });

  // A poll that resolves after the dialog was closed, or after something else
  // settled the flow, must not resurrect it. Every polling panel has this race
  // and most find it in production.
  it.each<DeviceFlowPhase>([
    { kind: "idle" },
    { kind: "starting" },
    { kind: "connected", accountName: "Dallas" },
    { kind: "failed", reason: "gone" },
  ])("leaves a settled phase alone (%s)", (phase) => {
    expect(deviceFlowAfterPoll(phase, { state: "pending" })).toBe(phase);
    expect(deviceFlowAfterPoll(phase, { state: "connected" })).toBe(phase);
  });

  // A future server growing a fourth word must not leave a dialog polling
  // forever against something it cannot read.
  it("stops rather than spins on a state this build does not know", () => {
    const next = deviceFlowAfterPoll(waiting, { state: "quantum" } as unknown as DevicePoll);
    expect(next.kind).toBe("failed");
    expect(deviceFlowIsPolling(next)).toBe(false);
  });
});

describe("deviceSecondsRemaining", () => {
  const at = Date.parse("2026-08-19T12:30:00Z");

  it("counts down whole seconds", () => {
    expect(deviceSecondsRemaining(auth.expiresAt, at - 90_000)).toBe(90);
    expect(deviceSecondsRemaining(auth.expiresAt, at - 1_500)).toBe(1);
  });

  it("floors at zero rather than going negative", () => {
    expect(deviceSecondsRemaining(auth.expiresAt, at + 60_000)).toBe(0);
  });

  // "No deadline known" is not "expired". Guessing a deadline would end a
  // working flow early, which is strictly worse than letting the server's own
  // expiry answer stop it.
  it.each([
    ["absent", undefined],
    ["unparseable", "not a date"],
  ])("returns null for an %s expiry rather than assuming one", (_name, value) => {
    expect(deviceSecondsRemaining(value, at)).toBeNull();
    expect(deviceHasExpired(value, at)).toBe(false);
  });

  // Expiry is measured in WHOLE seconds, so the last fractional second counts
  // as gone. That is the right rounding for this flow rather than an accident
  // of the arithmetic: the poll floor is five seconds, so a code with 900ms
  // left can never be polled again before it dies, and calling it alive would
  // buy exactly one wasted request against the operator's rate limit.
  it("reports expiry from the last whole second before the deadline", () => {
    expect(deviceHasExpired(auth.expiresAt, at)).toBe(true);
    expect(deviceHasExpired(auth.expiresAt, at - 1)).toBe(true);
    expect(deviceHasExpired(auth.expiresAt, at - 1_000)).toBe(false);
  });
});

describe("deviceCountdown", () => {
  it.each([
    [0, "0:00"],
    [9, "0:09"],
    [60, "1:00"],
    [61, "1:01"],
    [1800, "30:00"],
  ])("renders %d seconds as %s", (seconds, want) => {
    expect(deviceCountdown(seconds)).toBe(want);
  });

  it("never renders a negative, which would read as a clock running backwards", () => {
    expect(deviceCountdown(-5)).toBe("0:00");
  });
});
