/* The bandwidth calculator's formulas, in one place. /calculator and the
 * homepage calculator both import this, so the two cannot disagree.
 *
 * The model is polyemesis's, not a generic multistream sum: video is copied, so
 * the same bitstream goes to every destination, and the two legs are different
 * connections (encoder to server, server to platforms). Rates are kbps. */

/** Defaults for protocol overhead and headroom, in percent. /calculator opens with them and the
 *  homepage calculator applies them; both read these two constants. */
export const OVERHEAD_PCT = 5;
export const HEADROOM_PCT = 20;

/** Encoder to polyemesis: the video plus one audio rate per track. */
export function ingestKbps(i: { video: number; audio: number; tracks: number }): number {
  return i.video + i.audio * i.tracks;
}

/** polyemesis to the platforms: every destination sends the copied video and one audio pair. */
export function egressKbps(i: { video: number; audio: number; dests: number }): number {
  return i.dests * (i.video + i.audio);
}

/** What to provision: protocol overhead and headroom, both in percent. */
export function withMargins(kbps: number, overheadPct: number, headroomPct: number): number {
  return kbps * (1 + overheadPct / 100) * (1 + headroomPct / 100);
}

/** Decimal GB moved in `hours`. Overhead counts, headroom does not: headroom is capacity you
 *  provision, not bytes you send. 1 Mbps for one hour is 450 MB (1e6 bit/s x 3600 s / 8). */
export function gbFor(kbps: number, hours: number, overheadPct: number): number {
  return (((kbps * (1 + overheadPct / 100)) / 1000) * 450 * hours) / 1000;
}

/** A month is 4.35 weeks. */
export function perMonth(perStreamGb: number, perWeek: number): number {
  return perStreamGb * perWeek * 4.35;
}

export const fmtRate = (kbps: number): string => `${(kbps / 1000).toFixed(1)} Mbps`;

export const fmtData = (gb: number): string =>
  gb >= 1000 ? `${(gb / 1000).toFixed(2)} TB` : `${gb.toFixed(1)} GB`;

/** Read a count: an integer inside [min, max], by construction. Empty or non-numeric text gives
 *  `fallback`; a decimal is rounded; a value outside the range is clamped. */
export function clampInt(raw: string | number, min: number, max: number, fallback: number): number {
  const text = String(raw).trim();
  const n = text === "" ? NaN : Number(text);
  if (!Number.isFinite(n)) return fallback;
  return Math.min(max, Math.max(min, Math.round(n)));
}
