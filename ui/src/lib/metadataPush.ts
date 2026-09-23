/* The go-live composer's wire shapes and its push-poll rule.
 *
 * The shapes mirror internal/api/metadata.go. They used to live in
 * pages/Dashboard.tsx beside a hand-rolled fetch of their own; they are here so
 * lib/api.ts can type the metadata routes, which now go through request() like
 * every other call -- the hand-rolled copy JSON.parse'd a proxy's HTML 502 into
 * "Unexpected token '<'" and never told lib/session.ts about a 401. */

import { ApiError } from "@/lib/api";
import type { MetaField } from "@/lib/types";

/** What YouTube will still accept on the current broadcast.
 *
 *  Fetched when the composer opens, never polled: every row is a live platform
 *  call. A row that failed to read disables nothing -- the write still happens
 *  and the platform's 403 remains the authority. */
export interface BroadcastWindowRow {
  accountId: number;
  platform: string;
  accountName: string;
  window?: {
    broadcastId: string;
    title: string;
    lifeCycleStatus: string;
    contentDetailsLocked: boolean;
    lockedReason?: string;
  };
  /** false for a platform with no broadcast concept, which is not an error. */
  supported: boolean;
  error?: string;
}
export type MetaState = "pending" | "ok" | "partial" | "error";

export interface MetaCaps {
  fields: MetaField[];
  categoryLabel?: string;
  categoryHint?: string;
  titleMax?: number;
  descriptionMax?: number;
}

export interface MetaTarget {
  accountId: number;
  platform: string;
  accountName: string;
  caps: MetaCaps;
  /** Obligation metadata resolved from this account's destinations, sent on
   *  every push whether or not anything is typed here. Absent when no
   *  destination on the account set any. Mirrors internal/api's metadataTarget;
   *  the stream key it resolves alongside is deliberately never serialised. */
  compliance?: {
    privacy?: string;
    madeForKids?: boolean;
    labels?: Record<string, boolean>;
    facebookPrivacy?: string;
  };
}

export interface MetaOutcome {
  accountId: number;
  platform: string;
  accountName: string;
  state: MetaState;
  message?: string;
  applied: MetaField[];
  skipped?: MetaField[];
  target?: string;
  category?: string;
  warnings?: string[];
}

export interface MetaJob {
  id: string;
  done: boolean;
  results: MetaOutcome[];
  metadata: { title: string; description: string; category: string };
}

/** What the composer sends. Every broadcast field is optional because an
 *  omitted one means "leave it alone" on the platform, not "clear it". */
export interface MetaPushRequest {
  title: string;
  description: string;
  category: string;
  tags?: string[];
  broadcast: {
    tags?: string[];
    scheduledStart?: string;
    enableDvr?: boolean;
    enableAutoStart?: boolean;
    enableAutoStop?: boolean;
  };
}

/* WHEN TO STOP POLLING A PUSH.
 *
 * Push jobs live in the server's memory (api/metadata.go), so a restart
 * mid-push -- an upgrade, a crash, a reschedule -- loses the job, and GET
 * /metadata/push/{id} answers 404 for ever after. The composer used to poll
 * that every 1.2 s, swallow each failure, and hold Push disabled on "Pushing…"
 * until the tab was reloaded.
 *
 * So a poll failure has two terminal readings. A 404 is the server saying the
 * job is gone: stop at once, because no retry can bring it back. Anything else
 * -- a proxy 502 while the server restarts, a dropped connection -- might be a
 * blip, so it is retried, but only MAX_POLL_FAILURES times in a row: past
 * that, the server that comes back will not have the job either. Either way
 * the result is "status lost", which is what the operator is told, and Push is
 * usable again. */

/** Consecutive non-404 failures before the push is declared lost. At the
 *  composer's 1.2 s interval this is about twelve seconds of silence. */
export const MAX_POLL_FAILURES = 10;

/** What to do after a failed poll: keep going, or give the push up as lost.
 *  `failures` counts this one. */
export function pollVerdict(err: unknown, failures: number): "retry" | "lost" {
  if (err instanceof ApiError && err.status === 404) return "lost";
  return failures >= MAX_POLL_FAILURES ? "lost" : "retry";
}
