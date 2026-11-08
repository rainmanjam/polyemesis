import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import type { ActivitySourcesResponse } from "@/lib/types";

/** Source health changes on the order of seconds to minutes (a reconnect, a
 *  token that lapsed), and there is no socket frame for it, so it is read on a
 *  slow poll while the tab is open. */
const POLL_MS = 15_000;

/** GET /activity/sources, re-read every POLL_MS while mounted. `null` until the
 *  first answer; a failed read keeps the last good one and sets `failed`, so a
 *  blip does not blank the strip. Tolerates a body with fields missing. */
export function useActivitySources() {
  const [sources, setSources] = useState<ActivitySourcesResponse | null>(null);
  const [failed, setFailed] = useState(false);
  const live = useRef(true);

  const reload = useCallback(async () => {
    try {
      const r = await api.getActivitySources();
      if (!live.current) return;
      setSources({
        ...r,
        kinds: r.kinds ?? [],
        platforms: r.platforms ?? [],
        sources: r.sources ?? [],
      });
      setFailed(false);
    } catch {
      if (live.current) setFailed(true);
    }
  }, []);

  useEffect(() => {
    live.current = true;
    void reload();
    const id = window.setInterval(() => void reload(), POLL_MS);
    return () => {
      live.current = false;
      window.clearInterval(id);
    };
  }, [reload]);

  return { sources, failed, reload };
}
