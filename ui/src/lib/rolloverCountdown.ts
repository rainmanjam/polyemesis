/* ===========================================================================
   The planned-rollover countdown (#19), as the destination card reads it.

   The server states the platform's cap and when the session's cutoff falls
   (RolloverView.cutoffAt). This module decides what the card shows for that
   view, and nothing else: it never invents a cap, and it never shows a
   countdown for a cap polyemesis cannot stand behind.

   The rules, in order:
   - no limit for the platform            -> no countdown
   - limit is unverified, or 0 hours      -> no countdown (the number is not known)
   - the destination is not delivering    -> no countdown (nothing is counting)
   - delivering, but no cutoff yet        -> waiting for the platform's start
   - otherwise                            -> a countdown to the cutoff

   "reported" is a real countdown, but the card must say it is reported, not
   documented, so the state carries the status through.
   =========================================================================== */

import type { RolloverLimit, RolloverView } from "./types";

export type CountdownState =
  | { kind: "none"; reason: "no-limit" | "unverified" | "not-delivering" }
  | { kind: "waiting" }
  | {
      kind: "running";
      remainingMs: number;
      cutoff: Date;
      limit: RolloverLimit;
      reported: boolean;
    };

/** countdownFor decides the card's countdown state for one view at one moment. */
export function countdownFor(view: RolloverView, now: Date): CountdownState {
  const limit = view.limit;
  if (!limit) return { kind: "none", reason: "no-limit" };
  if (limit.status === "unverified" || !(limit.hours > 0)) {
    return { kind: "none", reason: "unverified" };
  }
  if (!view.delivering) return { kind: "none", reason: "not-delivering" };
  if (!view.cutoffAt) return { kind: "waiting" };
  const cutoff = new Date(view.cutoffAt);
  const remainingMs = Math.max(0, cutoff.getTime() - now.getTime());
  return {
    kind: "running",
    remainingMs,
    cutoff,
    limit,
    reported: limit.status === "reported",
  };
}

/** formatRemaining renders a duration as hours and minutes, or minutes alone
 *  when it is under an hour. Seconds are not shown: the card refreshes about
 *  once a minute, and a second-level tick would promise a precision the
 *  platform's cap does not have. */
export function formatRemaining(ms: number): string {
  const totalMinutes = Math.floor(Math.max(0, ms) / 60_000);
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  if (hours === 0) return `${minutes}m`;
  return `${hours}h ${String(minutes).padStart(2, "0")}m`;
}
