import { createContext, useContext, useMemo, useState } from "react";
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
  return {
    ok: () => setFailures(0),
    failed: () => setFailures((n) => n + 1),
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
