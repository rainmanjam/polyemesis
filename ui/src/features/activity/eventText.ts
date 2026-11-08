import type { Translator } from "@/lib/i18n";
import type { ActivityEvent, ActivitySource } from "@/lib/types";
import { tierLabel } from "./activityLib";

/** Who the row is about. A raid is about the channel that raided; a viewer who
 *  hid their identity is "Someone", not a blank. */
export function eventName(t: Translator, e: ActivityEvent): string {
  const who = e.kind === "raid" ? (e.other ?? e.actor) : e.actor;
  if (!who || who.anonymous || who.name.trim() === "") return t("activity.someone");
  return who.name;
}

/** The facts after the name: amount, tier, months, counts, title. Plain strings,
 *  so a screen reader reads them in order. */
export function eventDetails(t: Translator, e: ActivityEvent): string[] {
  const out: string[] = [];
  if (e.amount?.display) out.push(e.amount.display);
  if (e.tier) out.push(t("activity.row.tier", { tier: tierLabel(e.tier) }));
  if (e.months) out.push(t("activity.row.months", { months: e.months }));
  if (e.kind === "gift" && e.count) out.push(t("activity.row.subs", { count: e.count }));
  if (e.viewers) out.push(t("activity.row.viewers", { viewers: e.viewers }));
  if (e.level) out.push(t("activity.row.level", { level: e.level }));
  if (e.title) out.push(e.title);
  for (const x of [e.phase, e.status, e.action]) if (x) out.push(x);
  return out;
}

/** How many of a source's kinds are in each state that needs saying. "off" and
 *  "unknown" are not counted: off is the operator's choice, unknown is a source
 *  that has not reported yet. */
export function kindCounts(s: ActivitySource): { on: number; unavailable: number; reconnect: number } {
  const c = { on: 0, unavailable: 0, reconnect: 0 };
  for (const k of s.kinds ?? []) {
    if (k.state === "on") c.on++;
    else if (k.state === "unavailable") c.unavailable++;
    else if (k.state === "reconnect") c.reconnect++;
  }
  return c;
}
