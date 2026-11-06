import { useEffect, useState } from "react";

import { api } from "@/lib/api";
import { YOUTUBE_VERTICAL_POLL_MS } from "@/lib/youtubeVertical";
import type { DestinationId, YouTubeVerticalView } from "@/lib/types";

/* ===========================================================================
   The vertical half's YouTube health, for its sub-row (#7).

   IN hooks/, NOT BESIDE THE CARD, for the reason useFacebookStreamHealth.ts
   records: a module that exports a hook next to a component cannot hot-swap.

   POLLED ONCE A MINUTE, AND ONLY WHILE A HALF IS LIVE. The server refuses to
   read YouTube more often than that and reads nothing while both halves are
   stopped, so this poll is the client keeping its own traffic down to the same
   shape. A read that fails is kept on the view as healthError, not thrown: the
   sub-row shows it and the horizontal card is untouched.
   =========================================================================== */

/** The latest read of a vertical half. `null` until the first answer arrives,
 *  and after a pairing that does not exist, which is not an error. */
export function useYouTubeVerticalHealth(
  horizontalId: DestinationId,
  enabled: boolean,
): YouTubeVerticalView | null {
  const [view, setView] = useState<YouTubeVerticalView | null>(null);

  useEffect(() => {
    if (!enabled) return;
    let live = true;
    const read = async () => {
      try {
        const v = await api.youtubeVerticalStatus(horizontalId);
        if (live) setView(v);
      } catch {
        // No pair (404) or a transport failure: leave the last answer in place
        // rather than blanking the sub-row on a single missed poll.
      }
    };
    void read();
    const timer = setInterval(() => void read(), YOUTUBE_VERTICAL_POLL_MS);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [horizontalId, enabled]);

  return view;
}
