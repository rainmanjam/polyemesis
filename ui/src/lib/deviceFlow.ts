import type { DevicePoll, DeviceAuth } from "./types";

/* ===========================================================================
   The device code flow's client half, as data.

   A dialog that polls has three things it can get wrong, and all three are
   invisible in a screenshot: how long it waits between polls, when it stops,
   and what it says when the answer is not "connected". Every one of those is
   decided here rather than inside JSX, on the same reasoning viewerCount.ts
   sets out beside it — a branch taken in a component is a branch tested by
   rendering a component, which is the expensive way to find out that a live
   stream reads as an audience of nobody.

   WHAT MAKES THIS ONE WORTH SPLITTING OUT IN PARTICULAR. The failure it
   prevents is not a wrong pixel, it is a REQUEST STORM against the operator's
   own developer app: polling faster than the platform asked gets that app
   rate-limited for every feature at once, mid-connect, and the operator's only
   symptom is that connecting stopped working. The server enforces the interval
   as well (see internal/api/device_flow.go) — this is the client keeping its
   own side of the same promise, and the floor below is the reason the two
   agree.
   =========================================================================== */

/** The floor under whatever the server reports, in milliseconds.
 *
 *  FIVE SECONDS, AND IT IS A MIRROR RATHER THAN A CHOICE. internal/oauth's
 *  deviceMinPollInterval is 5s and already floors the platform's own answer, so
 *  the server can never legitimately ask for less; this exists for the answers
 *  that never came from the server at all — a missing field, a `0`, a response
 *  a reload never received. Without it, `retryInSeconds ?? 0` is a `setTimeout`
 *  of zero and a tab that polls as fast as the network allows.
 *
 *  It is deliberately not configurable. A knob here would be a knob for
 *  "how hard may this hammer the platform", which nobody should be turning. */
export const DEVICE_POLL_FLOOR_MS = 5_000;

/** How long to wait before the next poll.
 *
 *  Takes the LARGER of the floor and what the server asked for, so a platform
 *  that widens its interval mid-flow — which is what RFC 8628's slow_down is
 *  for — is obeyed, while a zero, a negative, a NaN or an absent value all land
 *  on the floor rather than on an immediate retry.
 *
 *  `Number.isFinite` rather than `?? `: a JSON `null`, a string, or an Infinity
 *  are all "the server did not say", and only an actual finite number is an
 *  answer. Same reading as viewerReadout's, for the same reason. */
export function devicePollDelayMs(retryInSeconds: number | undefined | null): number {
  if (typeof retryInSeconds !== "number" || !Number.isFinite(retryInSeconds)) {
    return DEVICE_POLL_FLOOR_MS;
  }
  return Math.max(DEVICE_POLL_FLOOR_MS, Math.round(retryInSeconds * 1000));
}

/** What the dialog is doing, as a closed set.
 *
 *  Five members rather than a couple of booleans, because a boolean pair admits
 *  states that cannot happen ("connected and still polling") and the compiler
 *  should refuse a renderer that forgets one of the real ones. */
export type DeviceFlowPhase =
  /** Nothing started. The operator has the button and nothing else. */
  | { kind: "idle" }
  /** The start request is in flight. No code to show yet. */
  | { kind: "starting" }
  /** The code is on screen and the client is polling. */
  | { kind: "waiting"; auth: DeviceAuth }
  /** Done. The account is connected and the caller should refresh its list. */
  | { kind: "connected"; accountName: string }
  /** Stopped, and it will not recover on its own. `reason` is the server's own
   *  sentence where there is one — never a paraphrase, because the server names
   *  the platform and a paraphrase would lose that. */
  | { kind: "failed"; reason: string };

/** Whether the phase should keep a timer running.
 *
 *  Extracted as its own predicate rather than inlined into an effect's guard,
 *  because "stop polling" is the property with the cost attached and a test can
 *  only assert it if it is a value. */
export function deviceFlowIsPolling(phase: DeviceFlowPhase): boolean {
  return phase.kind === "waiting";
}

/** The next phase, given the one it is in and what a poll answered.
 *
 *  A PURE REDUCER over the server's three words. The server's vocabulary is
 *  closed (see internal/api/device_flow.go) and this is the whole of the
 *  client's response to it, so a fourth word arriving from a future server
 *  lands in the default branch and stops the flow with a sentence, rather than
 *  leaving a dialog polling forever against something it does not understand. */
export function deviceFlowAfterPoll(phase: DeviceFlowPhase, res: DevicePoll): DeviceFlowPhase {
  // A poll that resolves after the operator closed the dialog, or after
  // something else already settled it, must not resurrect it. This is the
  // in-flight-request race that every polling panel has and most discover in
  // production.
  if (phase.kind !== "waiting") return phase;

  switch (res.state) {
    case "pending":
      return phase;
    case "connected":
      return {
        kind: "connected",
        // The name is what the operator recognises; the ref is a platform id
        // that means nothing to them. Empty is tolerated rather than treated
        // as a failure — the account IS connected, and refusing to say so
        // because a display name was blank would be the worse answer.
        accountName: res.account?.accountName ?? "",
      };
    case "expired":
      return { kind: "failed", reason: res.reason ?? "" };
    default:
      return {
        kind: "failed",
        reason: "The server answered with a state this version does not understand.",
      };
  }
}

/** Whole seconds left before the code dies, floored at zero.
 *
 *  Used to STOP, not just to display. A device code that has expired can only
 *  ever answer "invalid device code", so polling past `expiresAt` spends the
 *  operator's rate limit on a foregone conclusion — and leaves them watching a
 *  spinner that will never resolve, which is the worse half.
 *
 *  An absent or unparseable `expiresAt` returns null, meaning "no deadline
 *  known": the flow keeps polling and the server's own expiry answer stops it.
 *  Guessing a deadline would end a working flow early. */
export function deviceSecondsRemaining(expiresAt: string | undefined, now: number): number | null {
  if (!expiresAt) return null;
  const at = Date.parse(expiresAt);
  if (Number.isNaN(at)) return null;
  return Math.max(0, Math.floor((at - now) / 1000));
}

/** Whether the deadline has passed. Unknown deadlines are never expired. */
export function deviceHasExpired(expiresAt: string | undefined, now: number): boolean {
  const left = deviceSecondsRemaining(expiresAt, now);
  return left !== null && left <= 0;
}

/** `m:ss` for the countdown beside the code.
 *
 *  A countdown rather than an absolute time, because "expires at 19:42" makes
 *  an operator do arithmetic under mild pressure, and because the clock on the
 *  server and the clock on their phone need not agree. */
export function deviceCountdown(seconds: number): string {
  const safe = Math.max(0, Math.floor(seconds));
  const m = Math.floor(safe / 60);
  const s = safe % 60;
  return `${m}:${String(s).padStart(2, "0")}`;
}
