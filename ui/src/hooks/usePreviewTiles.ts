import { useEffect, useState } from "react";

/** One source's preview state, as GET /previews reports it. */
export interface PreviewTile {
  id: number;
  name: string;
  width?: number;
  height?: number;
  /** Anything reaching this programme's destinations: encoder, backup, slate
   *  or playlist. Decides whether a picture is worth showing. */
  outputLive: boolean;
  /** The operator's own encoder arriving. Labels the tile; it must not blank
   *  it, because during a failover this is false while something real is going
   *  out. */
  ingestLive: boolean;
  /** The tier on air: "primary", "backup", "slate", "playlist". */
  onAir?: string;
}

/** Polls the per-source preview telemetry.
 *
 *  POLLED, NOT PUSHED, and deliberately. The status WebSocket is not
 *  source-scoped: every engine publishes onto the same broker and the app keeps
 *  one status and one bitrate series, so a grid fed from it would redraw every
 *  tile from whichever engine spoke last -- the wrong picture's state under the
 *  right picture's name. Making that feed source-aware is a change to every
 *  producer and consumer of it, not to this grid.
 *
 *  The precedent is already here: PlayoutPage, MonitoringPage and MetersPage all
 *  poll for their own reasons rather than joining the always-on feed.
 */
export function usePreviewTiles(
  enabled: boolean,
  intervalMs = 2000,
): PreviewTile[] {
  const [tiles, setTiles] = useState<PreviewTile[]>([]);

  useEffect(() => {
    if (!enabled) {
      setTiles([]);
      return;
    }
    let cancelled = false;
    // SINGLE-FLIGHT, AND ORDERED. setInterval will start another request while a
    // slow one is still in the air, and the responses can land out of order --
    // an older outputLive:true overwriting a newer false lifts the opaque
    // overlay and puts the stale frame back on screen, which is the exact bug
    // this grid exists to fix.
    let inFlight = false;
    let latest = 0;
    const read = async () => {
      if (inFlight) return;
      inFlight = true;
      const seq = ++latest;
      try {
        const r = await fetch("/previews", { credentials: "same-origin" });
        if (!r.ok) return;
        const body = (await r.json()) as PreviewTile[];
        // Discard anything a later request has already answered.
        if (!cancelled && seq === latest && Array.isArray(body)) setTiles(body);
      } catch {
        // A failed poll leaves the last answer standing. Blanking the grid on
        // one dropped request would flicker every tile for a reload.
      } finally {
        inFlight = false;
      }
    };
    void read();
    const t = window.setInterval(read, intervalMs);
    return () => {
      cancelled = true;
      window.clearInterval(t);
    };
  }, [enabled, intervalMs]);

  return tiles;
}
