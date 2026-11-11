/** The words an alert is drawn with, one sentence per kind. Pure over a
 *  translator, so every kind's wording and its fallbacks are unit-tested
 *  without rendering anything. */

import type { Translator } from "./i18n";
import { tierLabel } from "@/features/activity/activityLib";
import type { ActivityDisplay } from "./types";

/** What the overlay hands the sentence: the display event, and the follower
 *  count when it stands for a collapsed follow raid. */
export interface AlertSubject {
  display: ActivityDisplay;
  followers?: number;
}

/** The name to draw: "" is anonymous or a name the gate blanked. */
export function alertName(t: Translator, d: Pick<ActivityDisplay, "name">): string {
  return d.name.trim() === "" ? t("activity.someone") : d.name;
}

function subscribeText(t: Translator, d: ActivityDisplay, name: string): string {
  return d.tier
    ? t("activity.say.subscribeTier", { name, tier: tierLabel(d.tier) })
    : t("activity.say.subscribe", { name });
}

function resubText(t: Translator, d: ActivityDisplay, name: string): string {
  if (!d.months) return t("activity.say.resubPlain", { name });
  return d.months === 1
    ? t("activity.say.resubOne", { name })
    : t("activity.say.resub", { name, months: d.months });
}

function giftText(t: Translator, d: ActivityDisplay, name: string): string {
  return d.count && d.count > 1
    ? t("activity.say.gift", { name, count: d.count })
    : t("activity.say.giftOne", { name });
}

function tipText(t: Translator, d: ActivityDisplay, name: string, showAmount: boolean): string {
  const amount = d.amount?.display;
  return showAmount && amount
    ? t("activity.say.tip", { name, amount })
    : t("activity.say.tipPlain", { name });
}

function raidText(t: Translator, d: ActivityDisplay, name: string): string {
  if (!d.viewers) return t("activity.say.raidPlain", { name });
  return d.viewers === 1
    ? t("activity.say.raidOne", { name })
    : t("activity.say.raid", { name, viewers: d.viewers });
}

function redemptionText(t: Translator, d: ActivityDisplay, name: string): string {
  return d.title
    ? t("activity.say.redemption", { name, title: d.title })
    : t("activity.say.redemptionPlain", { name });
}

function hypetrainText(t: Translator, d: ActivityDisplay): string {
  return d.level ? t("activity.say.hypetrain", { level: d.level }) : t("activity.say.hypetrainPlain");
}

/** The headline. `showAmount` false drops the figure but keeps the sentence,
 *  so a tip still reads as a tip. */
export function alertHeadline(t: Translator, s: AlertSubject, showAmount = true): string {
  const d = s.display;
  if (s.followers) return t("activity.say.followers", { count: s.followers });
  const name = alertName(t, d);
  switch (d.kind) {
    case "follow":
      return t("activity.say.follow", { name });
    case "subscribe":
      return subscribeText(t, d, name);
    case "resub":
      return resubText(t, d, name);
    case "gift":
      return giftText(t, d, name);
    case "tip":
      return tipText(t, d, name, showAmount);
    case "raid":
      return raidText(t, d, name);
    case "redemption":
      return redemptionText(t, d, name);
    case "hypetrain":
      return hypetrainText(t, d);
    case "shoutout":
      return t("activity.say.shoutout", { name });
    default:
      return t("activity.say.generic", { name });
  }
}
