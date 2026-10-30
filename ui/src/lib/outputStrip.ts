/* A10: the two-stage signal path on the routing page, source then output.
 *
 * SOURCE is the ingest meter (peak per channel, measured BEFORE any routing).
 * OUTPUT is the destination's own EBU R128 analyser (internal/meters): a
 * separate FFmpeg that reads the same hub and runs this destination's compiled
 * graph, so it measures the mix AFTER routing. It is the only post-mix reading
 * the server has, and it exists only while the loudness monitor is on and the
 * destination is running; every other case is a named state, never a blank or a
 * zero that could pass for silence. */

/** One destination's report, as GET /loudness returns it (meters.Report). */
export interface OutputReport {
  destinationId: number;
  seconds: number;
  momentaryLufs: number;
  truePeakDbtp: number;
  reason: string;
  at: string;
  error?: string;
}

export interface LoudnessFeed {
  enabled?: boolean;
  reports: OutputReport[];
}

/** meters.StaleAfter: a report with no new frame for this long is history. */
export const OUTPUT_STALE_MS = 10_000;
/** meters.LUFSFloor: below the absolute gate, the absence of a reading. */
export const LUFS_FLOOR = -70;
/** A true peak at or under this is silence. Digital silence is reported at the
 *  -120 dBTP floor; -90 leaves room for dither without calling it signal. */
export const SILENT_TRUE_PEAK = -90;
/** A source peak above this is unmistakably carrying programme. */
export const SOURCE_HOT_DB = -50;

export type OutputState =
  /** The monitor is switched off: nothing measures the output. */
  | { kind: "off" }
  /** The monitor is on but has no analyser for this destination (not running). */
  | { kind: "absent" }
  | { kind: "starting" }
  | { kind: "error"; error: string }
  | { kind: "stale"; report: OutputReport }
  | { kind: "live"; report: OutputReport };

export function outputState(
  feed: LoudnessFeed | null,
  destinationId: number,
  now: number,
): OutputState {
  if (!feed || feed.enabled === false) return { kind: "off" };
  const report = feed.reports.find((r) => r.destinationId === destinationId);
  if (!report) return { kind: "absent" };
  if (report.error) return { kind: "error", error: report.error };
  // meters.Starting has measured nothing yet: no programme time has passed.
  if (report.seconds <= 0) return { kind: "starting" };
  const at = Date.parse(report.at);
  if (!Number.isFinite(at) || now - at > OUTPUT_STALE_MS) return { kind: "stale", report };
  return { kind: "live", report };
}

/** The loudest ingest peak among the tracks this destination actually sends
 *  (routed and not muted), or null when nothing is metering them. */
export function sourcePeak(peaks: number[][] | null | undefined, tracks: number[]): number | null {
  let best: number | null = null;
  for (const t of tracks) {
    for (const db of peaks?.[t] ?? []) {
      if (Number.isFinite(db) && (best === null || db > best)) best = db;
    }
  }
  return best;
}

/** "Source hot, output silent": the routing is dropping a signal that arrives.
 *  Only on a LIVE output report, and judged by true peak, because a momentary
 *  loudness at the -70 floor means "below the gate", not "silent". */
export function routingDropsSignal(state: OutputState, source: number | null): boolean {
  if (state.kind !== "live" || source === null) return false;
  return source > SOURCE_HOT_DB && state.report.truePeakDbtp <= SILENT_TRUE_PEAK;
}
