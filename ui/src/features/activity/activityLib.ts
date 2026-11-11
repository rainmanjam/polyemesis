/** The activity feature's pure rules: kinds, amounts, the settings edits and
 *  the feed's merge. No React, no clock, no network, so each is unit-tested
 *  directly and the components only draw their answers. */

import type {
  ActivityAmount,
  ActivityEvent,
  ActivityKind,
  ActivitySettings,
  MinAmount,
} from "@/lib/types";
import { OVERLAY_ALERT_KINDS } from "@/lib/overlay";

/** Every kind, in the order the console lists them. */
export const ACTIVITY_KINDS: readonly ActivityKind[] = [
  "follow",
  "subscribe",
  "resub",
  "gift",
  "tip",
  "raid",
  "redemption",
  "hypetrain",
  "shoutout",
  "poll",
  "prediction",
  "stream",
  "moderation",
  "reaction",
];

/** Platforms the pickers offer before the server has named its own. */
export const FALLBACK_PLATFORMS = ["twitch", "youtube", "kick", "x", "rumble", "facebook"] as const;

/** The feed keeps this many rows; the server holds 500 at most. */
export const FEED_LIMIT = 500;

/** Whether an alert can be drawn on the overlay for this kind. The rest are
 *  console-only (feed, hooks), by the server's own design. */
export function isOverlayKind(kind: string): boolean {
  return (OVERLAY_ALERT_KINDS as readonly string[]).includes(kind);
}

// ------------------------------------------------------------------ amounts

/** The currency or unit an amount is in, and its size in that currency's major
 *  units (dollars, not cents; bits as they are). null for no amount. */
export function amountValue(a: ActivityAmount | undefined): { code: string; value: number } | null {
  if (!a) return null;
  if (a.currency && a.minor !== undefined) {
    return { code: a.currency.toUpperCase(), value: a.minor / 10 ** (a.exponent ?? 0) };
  }
  if (a.unit && a.units !== undefined) return { code: a.unit.toLowerCase(), value: a.units };
  return null;
}

/** A VIEW-ONLY filter: whether the feed hides this event for being under
 *  `min` of `code`. Only an amount in the SAME currency or unit is compared;
 *  an amount in anything else, or no amount at all, is never hidden. Nothing is
 *  converted -- a euro is not "worth" so many bits. */
export function belowMinimum(e: ActivityEvent, filter: { code: string; min: number } | null): boolean {
  // NaN is "not greater than zero" too, so it is spelled out rather than negated.
  if (!filter || Number.isNaN(filter.min) || filter.min <= 0) return false;
  const v = amountValue(e.amount);
  if (v?.code !== filter.code) return false;
  return v.value < filter.min;
}

/** The currencies and units present in the loaded events, for the filter's
 *  picker, most common first. */
export function amountCodes(events: readonly ActivityEvent[]): string[] {
  const n = new Map<string, number>();
  for (const e of events) {
    const v = amountValue(e.amount);
    if (v) n.set(v.code, (n.get(v.code) ?? 0) + 1);
  }
  return [...n].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])).map(([c]) => c);
}

/** Tier as viewers know it: Twitch's "1000" is Tier 1. */
export function tierLabel(tier: string): string {
  return /^[1-3]000$/.test(tier) ? tier[0] : tier;
}

// ------------------------------------------------------------- the feed list

/** Merges incoming events into the held list: newest first, de-duplicated by
 *  id (the first copy held wins, so a row does not jump), bounded. Used for
 *  the seed, a live frame and the catch-up after a reconnect alike. */
export function mergeEvents(
  held: readonly ActivityEvent[],
  incoming: readonly ActivityEvent[],
  limit = FEED_LIMIT,
): ActivityEvent[] {
  const seen = new Set(held.map((e) => e.id));
  const fresh: ActivityEvent[] = [];
  for (const e of incoming) {
    if (!e || typeof e.id !== "string" || seen.has(e.id)) continue;
    seen.add(e.id);
    fresh.push(e);
  }
  if (fresh.length === 0) return held as ActivityEvent[];
  const all = [...held, ...fresh];
  // Stable on ties, so equal timestamps keep their arrival order.
  const byTime = all
    .map((e, i) => ({ e, i, t: Date.parse(e.at) || 0 }))
    .sort((a, b) => b.t - a.t || a.i - b.i)
    .map((x) => x.e);
  return byTime.slice(0, limit);
}

/** How long ago, as a unit and a count, or null when over a day (the row then
 *  shows the clock). Never negative: a skewed clock reads as "now". */
export function ago(iso: string, now: number): { unit: "s" | "m" | "h"; n: number } | null {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return null;
  const s = Math.max(0, Math.floor((now - t) / 1000));
  if (s < 60) return { unit: "s", n: s };
  if (s < 3600) return { unit: "m", n: Math.floor(s / 60) };
  if (s < 86400) return { unit: "h", n: Math.floor(s / 3600) };
  return null;
}

// ------------------------------------------------------- the settings edits

/** What a platform uses when it has no entry: every kind but reactions. */
export function defaultKinds(all: readonly string[] = ACTIVITY_KINDS): string[] {
  return all.filter((k) => k !== "reaction");
}

/** The kinds a platform has on: its own list, or the defaults when it has
 *  none. [] is a real answer (everything off), not "absent". */
export function enabledKinds(
  map: ActivitySettings["kinds"],
  platform: string,
  all: readonly string[] = ACTIVITY_KINDS,
): string[] {
  const own = map?.[platform];
  return own ?? defaultKinds(all);
}

/** Turns one kind on or off for one platform. The platform gains an explicit
 *  list (starting from what it effectively had), kept in `all`'s order. */
export function setKind(
  map: ActivitySettings["kinds"],
  platform: string,
  kind: string,
  on: boolean,
  all: readonly string[] = ACTIVITY_KINDS,
): Record<string, string[]> {
  const have = new Set(enabledKinds(map, platform, all));
  if (on) have.add(kind);
  else have.delete(kind);
  const ordered = [...all.filter((k) => have.has(k)), ...[...have].filter((k) => !all.includes(k))];
  return { ...map, [platform]: ordered };
}

/** Puts a platform back on the defaults by REMOVING its key. Writing the
 *  default list out instead would pin it: a kind added later would then be off
 *  for that platform for ever. */
export function resetPlatform(
  map: ActivitySettings["kinds"],
  platform: string,
): Record<string, string[]> {
  const next = { ...map };
  delete next[platform];
  return next;
}

/** Whether a platform has its own list (so "back to defaults" has work to do). */
export function isCustomised(map: ActivitySettings["kinds"], platform: string): boolean {
  return map !== undefined && Object.hasOwn(map, platform);
}

// ---------------------------------------------------------- minimum amounts

export type MinProblem = "currency" | "unit" | "incomplete" | "both";

/** What is wrong with one minimum row, or null. Money (currency+minor) XOR a
 *  platform currency (unit+units); upper-case 3 letters / lower-case letters
 *  are applied by normaliseMinTip, so a value only needs to BECOME valid. */
export function minProblem(m: MinAmount): MinProblem | null {
  const money = m.currency !== undefined || m.minor !== undefined;
  const unit = m.unit !== undefined || m.units !== undefined;
  if (money && unit) return "both";
  if (money) {
    if (!/^[A-Za-z]{3}$/.test((m.currency ?? "").trim())) return "currency";
    if (m.minor === undefined || !Number.isFinite(m.minor) || m.minor < 0) return "incomplete";
    return null;
  }
  if (unit) {
    if (!/^[A-Za-z]+$/.test((m.unit ?? "").trim())) return "unit";
    if (m.units === undefined || !Number.isFinite(m.units) || m.units < 0) return "incomplete";
    return null;
  }
  return "incomplete";
}

/** The rows as the server wants them: currency upper-case, unit lower-case,
 *  integers, empty rows dropped. Applied on SAVE, never while typing, so the
 *  field does not fight the keyboard. */
export function normaliseMinTip(rows: readonly MinAmount[] | undefined): MinAmount[] {
  const out: MinAmount[] = [];
  for (const r of rows ?? []) {
    if (r.currency === undefined && r.minor === undefined && r.unit === undefined && r.units === undefined) continue;
    if (r.currency !== undefined || r.minor !== undefined) {
      out.push({ currency: (r.currency ?? "").trim().toUpperCase(), minor: Math.round(r.minor ?? 0) });
    } else {
      out.push({ unit: (r.unit ?? "").trim().toLowerCase(), units: Math.round(r.units ?? 0) });
    }
  }
  return out;
}

/** The first problem across the rows, with its index, or null. */
export function firstMinProblem(rows: readonly MinAmount[] | undefined): { index: number; problem: MinProblem } | null {
  for (const [index, r] of (rows ?? []).entries()) {
    const problem = minProblem(r);
    if (problem) return { index, problem };
  }
  return null;
}

/** The settings block as PUT: rows normalised, and `kinds` ALWAYS sent when
 *  the stored block had any, as `{}` when the last custom platform was reset --
 *  the server replaces the map wholesale, so omitting it would keep the old
 *  entries. */
export function activityForSave(draft: ActivitySettings, stored: ActivitySettings | undefined): ActivitySettings {
  const out: ActivitySettings = {
    enabled: draft.enabled,
    overlayMessages: draft.overlayMessages,
    reactions: draft.reactions,
  };
  const min = normaliseMinTip(draft.minTip);
  if (min.length > 0) out.minTip = min;
  else if (stored?.minTip?.length) out.minTip = [];
  if (draft.kinds && Object.keys(draft.kinds).length > 0) out.kinds = draft.kinds;
  else if (stored?.kinds && Object.keys(stored.kinds).length > 0) out.kinds = {};
  return out;
}

/** Whether two settings blocks say the same thing, for the save model's dirty
 *  test. Key order inside `kinds` does not matter. */
export function sameActivity(a: ActivitySettings, b: ActivitySettings): boolean {
  const norm = (s: ActivitySettings) =>
    JSON.stringify({
      e: s.enabled,
      m: s.overlayMessages,
      r: s.reactions,
      t: s.minTip ?? [],
      k: Object.entries(s.kinds ?? {}).sort(([x], [y]) => x.localeCompare(y)),
    });
  return norm(a) === norm(b);
}

/** The block a server with no activity settings yet reads as. */
export const DEFAULT_ACTIVITY_SETTINGS: ActivitySettings = {
  enabled: false,
  overlayMessages: true,
  reactions: false,
};
