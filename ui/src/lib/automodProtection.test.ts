import { describe, expect, it } from "vitest";

import {
  armedRungs,
  categoryEnabled,
  categoryHoldState,
  ceilingOf,
  detectorEnabled,
  detectorHolds,
  detectorLabelKey,
  durationParts,
  protectionOf,
  PROTECTION_CATEGORIES,
  withCategoryEnabled,
  withCeiling,
  withDetectorEnabled,
  withDetectorHold,
} from "./automodProtection";
import type { AutomodProtection } from "./types";

const cat = (id: string) => PROTECTION_CATEGORIES.find((c) => c.id === id)!;
const det = (c: string, id: string) => cat(c).detectors.find((d) => d.id === id)!;

const base = (over: Partial<AutomodProtection> = {}): AutomodProtection => ({
  state: "on",
  pack: "en",
  packVersion: 1,
  impersonation: {},
  strikeWindowSeconds: 600,
  ladderTimeoutSeconds: 600,
  ...over,
});

describe("the protection diff", () => {
  it("is sparse: a value equal to the pack default is not stored", () => {
    const off = withCategoryEnabled(base(), cat("doxxing"), false);
    expect(off.categories).toEqual({ doxxing: { enabled: false } });
    // Back to the default removes the entry rather than storing `true`.
    expect(withCategoryEnabled(off, cat("doxxing"), true).categories).toEqual({});
    // Profanity ships off, so switching it on is the stored value.
    expect(withCategoryEnabled(base(), cat("profanity"), true).categories).toEqual({
      profanity: { enabled: true },
    });
  });

  it("keeps a detector switch when its category is touched, and prunes when both are default", () => {
    let p = withDetectorEnabled(base(), cat("doxxing"), det("doxxing", "address"), true);
    expect(p.categories).toEqual({ doxxing: { detectors: { address: true } } });
    p = withCategoryEnabled(p, cat("doxxing"), false);
    expect(p.categories).toEqual({ doxxing: { enabled: false, detectors: { address: true } } });
    p = withCategoryEnabled(p, cat("doxxing"), true);
    p = withDetectorEnabled(p, cat("doxxing"), det("doxxing", "address"), false);
    expect(p.categories).toEqual({});
  });

  it("stores a hold override only when it differs from the pack", () => {
    const caps = det("flood", "caps");
    expect(detectorHolds(base(), caps)).toBe(false);
    const held = withDetectorHold(base(), caps, true);
    expect(held.holdDetectors).toEqual({ "flood/caps": true });
    expect(detectorHolds(held, caps)).toBe(true);
    expect(withDetectorHold(held, caps, false).holdDetectors).toEqual({});
    // The near-duplicate detector is keyed by its group, not its short name.
    expect(withDetectorHold(base(), det("flood", "near_duplicate"), true).holdDetectors).toEqual({
      spam_near_duplicate: true,
    });
  });

  it("stores a ceiling only when it differs", () => {
    expect(ceilingOf(base(), cat("scam"))).toBe("ban");
    const lowered = withCeiling(base(), cat("scam"), "timeout");
    expect(lowered.ceilings).toEqual({ scam: "timeout" });
    expect(ceilingOf(lowered, cat("scam"))).toBe("timeout");
    expect(withCeiling(lowered, cat("scam"), "ban").ceilings).toEqual({});
  });

  it("reads defaults from an empty block", () => {
    const p = base();
    expect(categoryEnabled(p, cat("hate_speech"))).toBe(true);
    expect(categoryEnabled(p, cat("profanity"))).toBe(false);
    expect(detectorEnabled(p, cat("doxxing"), det("doxxing", "address"))).toBe(false);
    expect(detectorEnabled(p, cat("doxxing"), det("doxxing", "phone"))).toBe(true);
    // The history checker's own detectors are not switchable here.
    expect(detectorEnabled(p, cat("flood"), det("flood", "repeats"))).toBe(true);
  });

  it("summarises what a category holds", () => {
    expect(categoryHoldState(base(), cat("doxxing"))).toBe("held");
    expect(categoryHoldState(base(), cat("flood"))).toBe("some");
    expect(categoryHoldState(base(), cat("profanity"))).toBe("held");
    let p = base();
    for (const d of cat("flood").detectors) p = withDetectorHold(p, d, false);
    expect(categoryHoldState(p, cat("flood"))).toBe("console");
  });

  it("falls back to an undecided block for a document that predates it", () => {
    expect(protectionOf(undefined).state).toBe("");
    expect(protectionOf({ protection: base() } as never).state).toBe("on");
  });
});

describe("small helpers", () => {
  it("formats the ladder timeout", () => {
    expect(durationParts(600)).toEqual({ key: "automod.protMinutes", n: 10 });
    expect(durationParts(90)).toEqual({ key: "automod.protSeconds", n: 90 });
    expect(durationParts(30)).toEqual({ key: "automod.protSeconds", n: 30 });
  });

  it("lists armed rungs lowest first", () => {
    expect(armedRungs({ "twitch/ban/protection": true, "twitch/delete/protection": true }, "twitch")).toEqual([
      "delete",
      "ban",
    ]);
    expect(armedRungs({ "twitch/ban/rules": true }, "twitch")).toEqual([]);
    expect(armedRungs(undefined, "kick")).toEqual([]);
  });

  it("labels a detector by its group, and not one it does not know", () => {
    expect(detectorLabelKey("doxxing/phone")).toBe("automod.protDetPhone");
    expect(detectorLabelKey("spam_near_duplicate")).toBe("automod.protDetNearDuplicate");
    expect(detectorLabelKey("repeats")).toBe("automod.protDetRepeats");
    expect(detectorLabelKey("nonsense")).toBeUndefined();
  });
});
