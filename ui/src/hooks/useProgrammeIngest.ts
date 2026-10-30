import { useEffect, useState } from "react";

import { useIngestLive, useLiveData } from "@/hooks/useLiveData";
import {
  programmeIngestLive,
  programmeSnapshot,
  relayRate,
  type RelaySample,
} from "@/lib/dashboardFacts";
import type { Status } from "@/lib/types";

/** The selected programme's ingest, as the Ingest card and the header chip
 *  both read it.
 *
 *  The status socket is install-wide: every engine publishes onto it and the
 *  app keeps the last frame, and the `stats` bitrate series names no programme
 *  at all. #924 scoped the dashboard's Ingest card to the programme selected
 *  in the header; the header's own chip, beside the switcher that selects it,
 *  went on reading the last frame and the unscoped series, so another
 *  programme's encoder turned this one's chip live. One hook for both, so the
 *  card and the chip cannot drift apart again.
 *
 *  - `snapshot`: the last frame that names the selected programme, or null
 *    until it reports (programmeSnapshot).
 *  - `kbps`: that programme's own relay throughput, from the byte counter its
 *    frames carry (relayRate); null until two of its frames are a second
 *    apart, and reset when the programme changes.
 *  - `live`: whether bytes are arriving for THIS programme
 *    (programmeIngestLive).
 *
 *  `sourceCount` is the caller's, because the two callers know it from
 *  different reads: the dashboard from /setup (null until known), the chrome
 *  from LiveData (0 until known). Both mean "unscoped" until there are two. */
export function useProgrammeIngest(sourceCount: number | null): {
  snapshot: Status | null;
  kbps: number | null;
  live: boolean;
} {
  const { status, programme } = useLiveData();
  const [held, setHeld] = useState<Status | null>(null);
  const [sample, setSample] = useState<RelaySample | null>(null);

  // Held only when there is a programme to hold it for. With none there is
  // nothing to scope to, every frame passes through (programmeSnapshot), and
  // reading it straight off the socket costs no render behind it.
  useEffect(() => {
    if (programme == null) return;
    setHeld((prev) => programmeSnapshot(prev, status, programme));
  }, [status, programme]);
  const snapshot = programme == null ? status : held;
  // A programme switch starts the rate over: the held baseline is the old
  // programme's counter.
  useEffect(() => {
    setSample(null);
  }, [programme]);
  useEffect(() => {
    setSample((prev) => relayRate(prev, snapshot?.relay?.rxBytes, Date.now()));
  }, [snapshot]);

  const kbps = sample?.kbps ?? null;
  const live = programmeIngestLive(useIngestLive(), sourceCount, snapshot?.source, kbps);
  return { snapshot, kbps, live };
}
