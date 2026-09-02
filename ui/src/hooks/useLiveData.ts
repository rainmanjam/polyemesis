import { createContext, useCallback, useContext, useMemo, useState } from "react";
import type {
  BitrateSample,
  Levels,
  LogLine,
  SourceInfo,
  SourceTrack,
  Status,
  SystemStats,
} from "@/lib/types";

/* ===========================================================================
   Reading live data. The socket that produces it lives in
   components/LiveDataProvider.tsx.

   This half stayed at @/hooks/useLiveData deliberately. Eleven files import a
   hook from this path and exactly one imports the provider, so splitting the
   provider out rather than the hooks leaves eleven imports untouched -- a
   refactor whose diff is one line per real change rather than twelve lines of
   churn that reviewers have to read past.

   The split itself is a Fast Refresh requirement: a file exporting both the
   provider component and these hooks cannot be hot-swapped, so editing any hook
   here used to tear down the WebSocket and re-seed from REST. Losing the live
   connection on every keystroke is a bad way to develop a live dashboard.
   =========================================================================== */

export interface LiveData {
  /** WHICH PROGRAMME the console is looking at, resolved once by
   *  LiveDataProvider from what the operator last looked at, else the server's
   *  first source. Null on an install with no sources, which the routes accept
   *  because with no sources there is no ambiguity to refuse.
   *
   *  Exposed because it is not only the live feed that needs it: every write to
   *  a programme-scoped route has to name one, and lib/autoApi.ts's callers had
   *  no way to reach this answer at all. Clips and the loudness monitor were
   *  refused outright on any install with two programmes as a result. There is
   *  one resolution rule and this is it -- a second one computed elsewhere is
   *  how two screens come to disagree about which show they are describing. */
  programme: number | null;
  /** Whether the programme question has been ANSWERED -- including the answer
   *  "there is none", which is legitimate on a fresh install.
   *
   *  Distinct from `programme != null` and the distinction is the whole point:
   *  null means EITHER "no sources exist" (a scoped route accepts that, there
   *  is no ambiguity to refuse) OR "not resolved yet" (a scoped route refuses
   *  it with 400 source_required). A control that fires during the second
   *  reads to the operator as a broken button -- it is how #606 kept
   *  reappearing after the client was fixed. Gate the control on this, not on
   *  the id. */
  programmeKnown: boolean;
  /** Whether the FIRST status snapshot has arrived from the socket.
   *
   *  Distinct from `status != null` in exactly the way programmeKnown above is
   *  distinct from `programme != null`, and for the same reason: every consumer
   *  reads status through `?.`, and `?.` on a null status is indistinguishable
   *  from a loaded status that holds nothing. A page that draws a CONCLUSION
   *  from that -- "no destinations yet", "0 kbps", "OFF AIR" -- states as fact
   *  something it has not been told, and states the opposite a second later
   *  when the snapshot lands.
   *
   *  Gate any decided empty state or zero measurement on this. Chrome, nav and
   *  anything else known without the server may render immediately; it is only
   *  claims about the install that have to wait. #663. */
  snapshotKnown: boolean;
  /** How many programmes this install has.
   *
   *  The console follows ONE at a time and has no switcher, so a page that
   *  shows programme-scoped figures cannot say whether it is showing the
   *  install or a slice of it. This is what lets a page label itself only when
   *  the label carries information — on the single-source install that is the
   *  overwhelming majority, a name that never varies is furniture. */
  sourceCount: number;
  /** Re-read /sources and re-resolve the programme.
   *
   *  Call after creating or deleting a source. The provider also re-resolves
   *  on its own when the status socket names a programme it has not seen --
   *  that covers the changes this tab did not make; this is the fast path for
   *  the ones it did. #646. */
  refreshSources: () => Promise<void>;
  connected: boolean;
  status: Status | null;
  source: SourceInfo | null;
  levels: Levels | null;
  system: SystemStats | null;
  bitrate: BitrateSample[];
  logs: LogLine[];
  /** Bumped whenever the server says the recordings list changed. */
  recordingsRevision: number;
  /** A frame arrived on the socket that could not be parsed as JSON and was
   *  dropped. `connected` alone cannot say this: the socket is open and
   *  `onclose` never fires, so nothing else tells the operator that whatever
   *  that frame was carrying -- a status, a level, a log line -- never landed.
   *  Sticky rather than self-clearing on the next good frame: a console that
   *  quietly ate one malformed message once is a console worth doubting for
   *  the rest of the session, not a blip to forget the instant the socket
   *  recovers. */
  frameError: boolean;
  clearLogs: () => void;
}

/** Consumed by useLiveData below and populated by LiveDataProvider. Exported
 *  only so the provider can reach it; nothing else should. */
export const LiveDataContext = createContext<LiveData | null>(null);

export function useLiveData(): LiveData {
  const ctx = useContext(LiveDataContext);
  if (!ctx) throw new Error("useLiveData must be used inside <LiveDataProvider>");
  return ctx;
}

/** Whether a broadcast is actually going out right now.
 *
 *  One definition, in one place, because several pages need to ask and the
 *  obvious answer is wrong. `status.ingest.state === "running"` is NOT it: an
 *  SRT listener sits in "running" the whole time it is merely waiting for an
 *  encoder to connect, so a page trusting that would warn about dropping a
 *  stream that does not exist — and an operator who is warned about nothing
 *  stops reading warnings.
 *
 *  The honest signal is bytes arriving: the layout has been probed AND the
 *  recent bitrate is non-zero. Both, because the probe survives the publisher
 *  going away and the bitrate series briefly reads zero between reconnects.
 *
 *  Errs towards saying NOT live. A missed warning costs a stream that was
 *  probably about to be reconfigured anyway; a false one trains the operator to
 *  click through the real thing. */
export function useIngestLive(): boolean {
  const { source, bitrate } = useLiveData();
  if (!source?.probed) return false;
  // The last few seconds only. A stream that stopped a minute ago leaves a
  // long tail of samples behind it.
  const recent = bitrate.slice(-5);
  return recent.some((s) => s.kbps > 0);
}

/** Tracks whether a repeating fetch is currently succeeding.
 *
 *  A poll that fails may be silent -- raising a toast every two seconds because
 *  the network hiccuped is worse than the hiccup. But silence must not be
 *  indefinite: after a few consecutive failures the panel is showing data that
 *  is no longer current, and nothing on screen says so. The operator reads a
 *  bitrate that stopped updating five minutes ago as a bitrate.
 *
 *  So: silent while it might recover, explicit once it clearly has not. The
 *  threshold is consecutive failures rather than elapsed time, because a poll
 *  that is merely slow is not a poll that is broken. */
export function useStaleTracker(threshold = 3) {
  const [failures, setFailures] = useState(0);
  /* STABLE IDENTITIES, because every caller puts these inside a polling
   * effect. Fresh closures each render made the effect's dependency list
   * unsatisfiable: naming `freshness` restarted the poll on every render, so
   * both callers left it out and took an exhaustive-deps warning instead --
   * and a suppressed dependency warning on a polling effect is precisely the
   * stale-closure shape that made #606 and #612 possible. Memoised, the
   * honest dependency list is also the correct one. */
  const ok = useCallback(() => setFailures(0), []);
  const failed = useCallback(() => setFailures((n) => n + 1), []);
  return {
    ok,
    failed,
    /** True once the data on screen should no longer be trusted as current. */
    stale: failures >= threshold,
    failures,
  };
}

/** The ingest track layout, falling back to six stereo tracks before the
 *  stream has been probed so the routing editor always has something to draw. */
export function useSourceTracks(): SourceTrack[] {
  const { source } = useLiveData();
  return useMemo<SourceTrack[]>(() => {
    const tracks = source?.tracks;
    if (tracks && tracks.length > 0) return tracks;
    return Array.from({ length: 6 }, (_, i) => ({
      index: i,
      channels: 2,
      codec: "aac",
      layout: "stereo",
    }));
  }, [source]);
}
