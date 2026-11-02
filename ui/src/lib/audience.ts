import type { AnalyticsPoint, AnalyticsReading } from "@/lib/types";

/** The public player's platform name on an analytics reading. */
export const PLAYER = "player";

const LABELS: Record<string, string> = {
  youtube: "YouTube",
  twitch: "Twitch",
  kick: "Kick",
  facebook: "Facebook",
  rumble: "Rumble",
};

/** A platform's display name. The player's row is labelled by the caller,
 *  through the catalogue, because it is a phrase rather than a brand. */
export function platformLabel(platform: string): string {
  return LABELS[platform] ?? platform;
}

/** The count a reading carries, or null when the platform did not report one.
 *  Null is never shown as 0: the caller renders "not reported". */
export function readingCount(r: AnalyticsReading): number | null {
  return typeof r.viewers === "number" ? r.viewers : null;
}

/** Sum of the readings that carry a count, null when none does. Mirrors the
 *  server's analytics.Total; used where the client groups rows itself. */
export function sumReported(rs: AnalyticsReading[]): number | null {
  let sum = 0;
  let n = 0;
  for (const r of rs) {
    const c = readingCount(r);
    if (c === null) continue;
    sum += c;
    n++;
  }
  return n === 0 ? null : sum;
}

/** Platform rows first in a stable order, the player last. */
export function orderReadings(rs: AnalyticsReading[]): AnalyticsReading[] {
  return [...rs].sort((a, b) => {
    const pa = a.platform === PLAYER;
    const pb = b.platform === PLAYER;
    if (pa !== pb) return pa ? 1 : -1;
    if (a.platform !== b.platform) return a.platform < b.platform ? -1 : 1;
    return (a.name ?? "").localeCompare(b.name ?? "");
  });
}

/** One decimal for an average, and no trailing ".0". */
export function formatAverage(v: number): string {
  return Number.isInteger(v) ? String(v) : v.toFixed(1);
}

/** The sparkline as SVG path data, in a w x h box. A point with no total is a
 *  GAP: the line lifts and starts again after it, so a stretch where nothing
 *  reported never reads as a dip to zero. A lone point between gaps is drawn
 *  as a dot-length segment so it is still visible. Empty when nothing ever
 *  reported. */
export function sparkPath(points: AnalyticsPoint[], w: number, h: number): string {
  const n = points.length;
  if (n === 0) return "";
  let max = 0;
  for (const p of points) if (typeof p.total === "number" && p.total > max) max = p.total;
  const x = (i: number) => (n === 1 ? w / 2 : (i / (n - 1)) * w);
  const y = (v: number) => (max === 0 ? h : h - (v / max) * h);
  const parts: string[] = [];
  let open = false;
  points.forEach((p, i) => {
    if (typeof p.total !== "number") {
      open = false;
      return;
    }
    const px = x(i).toFixed(1);
    const py = y(p.total).toFixed(1);
    if (!open) {
      parts.push(`M${px} ${py}`);
      const next = points[i + 1];
      if (!next || typeof next.total !== "number") parts.push(`h0.1`);
      open = true;
    } else {
      parts.push(`L${px} ${py}`);
    }
  });
  return parts.join(" ");
}
