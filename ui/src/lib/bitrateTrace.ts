import type { BitrateSample } from "./types";

/** One point on the Monitoring bitrate trace. A null kbps is a hole: the line
 *  breaks there instead of being drawn across a period nobody measured. */
export interface TracePoint {
  t: number;
  kbps: number | null;
}

/** internal/stats samples once a second. Three missed samples is a hole, not
 *  jitter: a busy host can deliver one late, not three. */
export const BITRATE_GAP_MS = 3000;

/** The samples as chart points, with a null inserted inside every hole longer
 *  than BITRATE_GAP_MS so a linear trace shows the hole as a hole. */
export function bitrateTrace(samples: readonly BitrateSample[]): TracePoint[] {
  const out: TracePoint[] = [];
  let prev: number | null = null;
  for (const s of samples) {
    const t = new Date(s.t).getTime();
    if (prev !== null && t - prev > BITRATE_GAP_MS) {
      out.push({ t: prev + (t - prev) / 2, kbps: null });
    }
    out.push({ t, kbps: Math.round(s.kbps) });
    prev = t;
  }
  return out;
}
