/* ===========================================================================
   What a recurring or rollover action's HTTP status means to the operator.

   The screens must not blur these together. A 409 is the server refusing for a
   reason the operator can act on (the show started, the destination is live,
   rollover is too soon). A 502 is the platform answering and refusing. A 500 is
   polyemesis failing. Each gets its own key, so the screen can say which.
   =========================================================================== */

import { ApiError } from "./api";

/** The outcome of running a rollover now (POST .../rollover/run). */
export type RunOutcome =
  | { kind: "rolled" }
  | { kind: "refused" }
  | { kind: "platform" }
  | { kind: "missing" }
  | { kind: "server" }
  | { kind: "other"; status: number };

/** classifyRun maps the status of a manual rollover to one outcome. 200 is the only success. */
export function classifyRun(err: unknown): RunOutcome {
  if (err == null) return { kind: "rolled" };
  if (!(err instanceof ApiError)) return { kind: "server" };
  switch (err.status) {
    case 409:
      return { kind: "refused" };
    case 502:
      return { kind: "platform" };
    case 404:
      return { kind: "missing" };
    case 500:
      return { kind: "server" };
    default:
      return { kind: "other", status: err.status };
  }
}

/** The outcome of skipping one occurrence. Only the 409 and 502 cases differ from a plain failure. */
export type SkipOutcome = { kind: "skipped" } | { kind: "refused" } | { kind: "platform" } | { kind: "failed" };

export function classifySkip(err: unknown): SkipOutcome {
  if (err == null) return { kind: "skipped" };
  if (err instanceof ApiError && err.status === 409) return { kind: "refused" };
  if (err instanceof ApiError && err.status === 502) return { kind: "platform" };
  return { kind: "failed" };
}
