import { describe, expect, it } from "vitest";
import en from "./i18n/en.json";
import { alertHeadline, alertName } from "./alertText";
import type { Translator } from "./i18n";
import type { ActivityDisplay } from "./types";

// The English catalogue through the real placeholder rule, no React.
const t = ((key: keyof typeof en, params?: Record<string, string | number>) =>
  (en[key] as string).replace(/\{(\w+)\}/g, (_, k) => String(params?.[k] ?? `{${k}}`))) as unknown as Translator;

const d = (over: Partial<ActivityDisplay>): ActivityDisplay => ({
  id: "x", platform: "twitch", kind: "follow", name: "Mara", at: "", ...over,
});
const say = (over: Partial<ActivityDisplay>, followers?: number, showAmount = true) =>
  alertHeadline(t, { display: d(over), followers }, showAmount);

describe("alert wording", () => {
  it("says Someone for an empty or blank name", () => {
    expect(alertName(t, { name: "" })).toBe("Someone");
    expect(alertName(t, { name: "   " })).toBe("Someone");
    expect(say({ name: "" })).toBe("Someone followed");
  });

  it("has a sentence per kind", () => {
    expect(say({ kind: "follow" })).toBe("Mara followed");
    expect(say({ kind: "subscribe" })).toBe("Mara subscribed");
    expect(say({ kind: "subscribe", tier: "1000" })).toBe("Mara subscribed (Tier 1)");
    expect(say({ kind: "subscribe", tier: "2" })).toBe("Mara subscribed (Tier 2)");
    expect(say({ kind: "resub" })).toBe("Mara resubscribed");
    expect(say({ kind: "resub", months: 1 })).toBe("Mara resubscribed for 1 month");
    expect(say({ kind: "resub", months: 8 })).toBe("Mara resubscribed for 8 months");
    expect(say({ kind: "gift" })).toBe("Mara gifted a sub");
    expect(say({ kind: "gift", count: 1 })).toBe("Mara gifted a sub");
    expect(say({ kind: "gift", count: 5 })).toBe("Mara gifted 5 subs");
    expect(say({ kind: "raid" })).toBe("Mara raided");
    expect(say({ kind: "raid", viewers: 1 })).toBe("Mara raided with 1 viewer");
    expect(say({ kind: "raid", viewers: 42 })).toBe("Mara raided with 42 viewers");
    expect(say({ kind: "redemption" })).toBe("Mara redeemed a reward");
    expect(say({ kind: "redemption", title: "Hydrate" })).toBe("Mara redeemed Hydrate");
    expect(say({ kind: "hypetrain" })).toBe("Hype train!");
    expect(say({ kind: "hypetrain", level: 3 })).toBe("Hype train level 3");
    expect(say({ kind: "shoutout" })).toBe("Shoutout from Mara");
    expect(say({ kind: "poll" })).toBe("Mara: new activity");
  });

  it("draws a tip's figure only when asked, and falls back to a plain tip", () => {
    const tip = { kind: "tip", amount: { display: "$5.00" } } as const;
    expect(say(tip)).toBe("Mara sent $5.00");
    expect(say(tip, undefined, false)).toBe("Mara sent a tip");
    expect(say({ kind: "tip" })).toBe("Mara sent a tip");
  });

  it("names a collapsed follow raid by its count, not by a person", () => {
    expect(say({ name: "" }, 12)).toBe("12 new followers");
  });
});
