import { useCallback, useEffect, useRef, useState } from "react";

import { api } from "@/lib/api";
import { failedRead, okRead, pendingRead, type ReadState } from "@/lib/readState";
import type { ChatFlagsResult } from "@/lib/types";

/** How often the Flagged count refreshes on its own. There is no live event for
 *  a flag, and a count that only moved on reload would undercount a raid. */
export const FLAGS_REFRESH_MS = 30_000;

/** The whole held list, which the server caps at the same number. */
const OPEN_LIMIT = 500;

/* What automod flagged, for the Chat page's Flagged chip (UX Council A48).
 *
 * A ReadState, not a bare list: a failed read must not render as "Nothing
 * flagged", which is the exonerating answer -- the same rule the user card
 * follows for its scrollback. A refresh keeps the last good answer on screen
 * rather than flashing back to pending.
 *
 * The full list is fetched only while it is OPEN. Closed, the chip needs only
 * the server's per-platform `counts`, which come back whatever the limit, so
 * the poll asks for one entry. And no poll runs while the tab is hidden: a
 * background tab polling a 500-entry list every 30 s was the review's finding. */
export function useChatFlags(open: boolean): { read: ReadState<ChatFlagsResult>; reload: () => void } {
  const [read, setRead] = useState<ReadState<ChatFlagsResult>>(pendingRead());
  // Drops a stale reply: a slow answer landing after a newer one would put an
  // older count back on the chip.
  const seq = useRef(0);

  const reload = useCallback(() => {
    const mine = ++seq.current;
    api
      .chatFlags({ limit: open ? OPEN_LIMIT : 1 })
      .then((r) => {
        if (mine === seq.current) setRead(okRead(r));
      })
      .catch(() => {
        if (mine === seq.current) setRead(failedRead());
      });
  }, [open]);

  useEffect(() => {
    reload();
    const id = setInterval(() => {
      if (typeof document !== "undefined" && document.hidden) return;
      reload();
    }, FLAGS_REFRESH_MS);
    return () => clearInterval(id);
  }, [reload]);

  return { read, reload };
}
