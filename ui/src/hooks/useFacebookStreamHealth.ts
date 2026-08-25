import { useEffect, useState } from "react";

import { api, ApiError } from "@/lib/api";
import {
  FACEBOOK_STREAM_HEALTH_INTERVAL_MS,
  type DestinationId,
  type IngestStreamHealth,
} from "@/lib/types";

/* ===========================================================================
   Polling Facebook's stream health for one destination.

   IN hooks/ AND NOT BESIDE THE PANEL IT FEEDS, on the same reasoning
   lib/capabilities.ts records for itself: a module that exports a hook next to
   a component cannot hot-swap, and the cost lands on whoever is mid-edit. That
   is not hypothetical here — the panel this feeds sits on the dashboard beside
   a destination dialog, and a full reload discards a half-filled form. oxlint's
   react(only-export-components) flags it, and it was right to.

   POLLED, NOT SUBSCRIBED. PlayoutPage.tsx:101 polls the viewer count rather
   than joining the always-on telemetry feed, and MonitoringPage.tsx:434 and
   MetersPage.tsx:106 do the same. A new event type is not cheap: it has to be
   classified in internal/events AllTypes(), which is AST-guarded, and a missing
   entry is a Type nobody was forced to classify, sent unredacted to whatever
   principal is listening. This is a pane on one card; a poll is proportionate.
   =========================================================================== */

/** Where the poll has got to.
 *
 *  Five states rather than "data or not", because the interesting ones are the
 *  three kinds of nothing and they mean opposite things to an operator:
 *  Facebook has no ingest to describe, this server cannot be asked, and the ask
 *  failed. Collapsing them into an empty pane is how "your encoder is fine, the
 *  broadcast has not started" ends up looking like a fault. */
export type HealthState =
  | { kind: "loading" }
  /** Answered. `streams` may be empty, which is a real answer: a scheduled
   *  broadcast has no ingest yet, an ended one has none any more, and a live one
   *  reports nothing until Facebook's own four-second timeout fires. */
  | { kind: "ok"; streams: IngestStreamHealth[] }
  /** The server answered supported:false — this destination's platform
   *  publishes no stream health at all. */
  | { kind: "unsupported" }
  /** The route is not there. Stated, not alarmed about, and the poll stops. */
  | { kind: "unavailable" }
  | { kind: "error"; detail: string };

/** Polls Facebook's stream health for one destination.
 *
 *  `enabled` is a parameter rather than a conditional call because hooks cannot
 *  be conditional, and it is what keeps the poll off every other card on the
 *  dashboard: only a Facebook destination with a live video id passes true.
 *
 *  THE INTERVAL IS FACEBOOK'S PUBLISHED FLOOR, quoted in full beside the
 *  constant in types.ts: "Stream health data refreshes every 2 seconds, so
 *  limit queries to no more than once every 2 seconds." Polling faster asks for
 *  the same numbers twice and spends somebody's rate limit doing it. */
export function useFacebookStreamHealth(
  destId: DestinationId,
  enabled: boolean,
): HealthState {
  const [state, setState] = useState<HealthState>({ kind: "loading" });

  useEffect(() => {
    if (!enabled) return;
    let live = true;
    // A 404 or 501 is not a blip, and retrying it is not optimism: it is a
    // request every two seconds, for ever, against a route that is not coming
    // back before a redeploy. The poll stops and the pane says so.
    let stopped = false;

    const read = async () => {
      try {
        const view = await api.facebookStreamHealth(destId);
        if (!live) return;
        setState(
          view.supported
            ? { kind: "ok", streams: view.streams ?? [] }
            : { kind: "unsupported" },
        );
      } catch (err) {
        if (!live) return;
        if (err instanceof ApiError && (err.status === 404 || err.status === 501)) {
          stopped = true;
          setState({ kind: "unavailable" });
          return;
        }
        // Every other failure keeps polling: one bad tick is not worth replacing
        // a good last reading with a red box two seconds before the next tick
        // recovers it. It is not swallowed either — silence would leave a stale
        // reading on screen reading as current.
        setState({
          kind: "error",
          detail: err instanceof Error ? err.message : String(err),
        });
      }
    };

    void read();
    const id = window.setInterval(() => {
      if (stopped) {
        window.clearInterval(id);
        return;
      }
      void read();
    }, FACEBOOK_STREAM_HEALTH_INTERVAL_MS);
    return () => {
      live = false;
      window.clearInterval(id);
    };
  }, [destId, enabled]);

  return state;
}
