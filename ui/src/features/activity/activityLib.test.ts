import { describe, expect, it } from "vitest";
import {
  activityForSave,
  ago,
  amountCodes,
  amountValue,
  belowMinimum,
  defaultKinds,
  enabledKinds,
  firstMinProblem,
  isCustomised,
  isOverlayKind,
  mergeEvents,
  minProblem,
  normaliseMinTip,
  resetPlatform,
  sameActivity,
  setKind,
  tierLabel,
  ACTIVITY_KINDS,
} from "./activityLib";
import type { ActivityEvent } from "@/lib/types";

const ev = (id: string, over: Partial<ActivityEvent> = {}): ActivityEvent => ({
  id,
  kind: "tip",
  platform: "twitch",
  at: "2026-11-07T20:00:00Z",
  source: { type: "t", id },
  ...over,
});

describe("amounts", () => {
  it("reads money in major units and units as they are", () => {
    expect(amountValue({ currency: "usd", minor: 500, exponent: 2 })).toEqual({ code: "USD", value: 5 });
    expect(amountValue({ currency: "JPY", minor: 500, exponent: 0 })).toEqual({ code: "JPY", value: 500 });
    expect(amountValue({ unit: "Bits", units: 100 })).toEqual({ code: "bits", value: 100 });
    expect(amountValue({ currency: "USD" })).toBeNull();
    expect(amountValue(undefined)).toBeNull();
  });

  it("hides below a minimum only in the same currency or unit", () => {
    const usd = (minor: number) => ev("u", { amount: { currency: "USD", minor, exponent: 2 } });
    const f = { code: "USD", min: 5 };
    expect(belowMinimum(usd(499), f)).toBe(true);
    expect(belowMinimum(usd(500), f)).toBe(false);
    // never converted: euros and bits are not compared with dollars
    expect(belowMinimum(ev("e", { amount: { currency: "EUR", minor: 1, exponent: 2 } }), f)).toBe(false);
    expect(belowMinimum(ev("b", { amount: { unit: "bits", units: 1 } }), f)).toBe(false);
    expect(belowMinimum(ev("n"), f)).toBe(false);
    expect(belowMinimum(ev("b2", { amount: { unit: "bits", units: 50 } }), { code: "bits", min: 100 })).toBe(true);
    expect(belowMinimum(usd(1), null)).toBe(false);
    expect(belowMinimum(usd(1), { code: "USD", min: 0 })).toBe(false);
  });

  it("lists the codes present, most common first", () => {
    const list = [
      ev("1", { amount: { unit: "bits", units: 1 } }),
      ev("2", { amount: { currency: "USD", minor: 1, exponent: 2 } }),
      ev("3", { amount: { unit: "bits", units: 2 } }),
      ev("4"),
    ];
    expect(amountCodes(list)).toEqual(["bits", "USD"]);
  });

  it("shows Twitch tiers as numbers", () => {
    expect(tierLabel("1000")).toBe("1");
    expect(tierLabel("3000")).toBe("3");
    expect(tierLabel("2")).toBe("2");
    expect(tierLabel("Prime")).toBe("Prime");
  });
});

describe("mergeEvents", () => {
  it("dedupes by id, keeps the first copy, and orders newest first", () => {
    const a = ev("a", { at: "2026-11-07T20:00:01Z" });
    const b = ev("b", { at: "2026-11-07T20:00:03Z" });
    const a2 = ev("a", { at: "2026-11-07T20:00:09Z", message: "dup" });
    const merged = mergeEvents([a], [b, a2]);
    expect(merged.map((e) => e.id)).toEqual(["b", "a"]);
    expect(merged[1].message).toBeUndefined();
  });

  it("returns the same list when nothing is new, and ignores junk", () => {
    const held = [ev("a")];
    expect(mergeEvents(held, [ev("a")])).toBe(held);
    expect(mergeEvents(held, [null as unknown as ActivityEvent, { id: 5 } as unknown as ActivityEvent])).toBe(held);
  });

  it("bounds the list, dropping the oldest", () => {
    const many = Array.from({ length: 12 }, (_, i) =>
      ev(`e${i}`, { at: new Date(Date.UTC(2026, 10, 7, 20, 0, i)).toISOString() }),
    );
    const merged = mergeEvents([], many, 5);
    expect(merged).toHaveLength(5);
    expect(merged[0].id).toBe("e11");
    expect(merged[4].id).toBe("e7");
  });
});

describe("ago", () => {
  const now = Date.parse("2026-11-07T20:10:00Z");
  it("picks the unit", () => {
    expect(ago("2026-11-07T20:09:50Z", now)).toEqual({ unit: "s", n: 10 });
    expect(ago("2026-11-07T20:05:00Z", now)).toEqual({ unit: "m", n: 5 });
    expect(ago("2026-11-07T17:10:00Z", now)).toEqual({ unit: "h", n: 3 });
    expect(ago("2026-11-05T17:10:00Z", now)).toBeNull();
    expect(ago("nope", now)).toBeNull();
    expect(ago("2026-11-07T20:11:00Z", now)).toEqual({ unit: "s", n: 0 });
  });
});

describe("the kinds map", () => {
  it("defaults to every kind except reactions", () => {
    expect(defaultKinds()).toHaveLength(ACTIVITY_KINDS.length - 1);
    expect(defaultKinds()).not.toContain("reaction");
    expect(enabledKinds(undefined, "twitch")).toEqual(defaultKinds());
    expect(enabledKinds({ kick: ["tip"] }, "twitch")).toEqual(defaultKinds());
  });

  it("treats an empty list as everything off, not as absent", () => {
    expect(enabledKinds({ twitch: [] }, "twitch")).toEqual([]);
  });

  it("starts an explicit list from the defaults when one kind is turned off", () => {
    const next = setKind(undefined, "twitch", "follow", false);
    expect(next.twitch).not.toContain("follow");
    expect(next.twitch).toContain("tip");
    expect(next.twitch).not.toContain("reaction");
  });

  it("turns reactions on in kind order and leaves other platforms alone", () => {
    const start = { kick: ["tip"] };
    const next = setKind(start, "facebook", "reaction", true);
    expect(next.facebook).toEqual([...defaultKinds(), "reaction"]);
    expect(next.kick).toEqual(["tip"]);
    expect(start).toEqual({ kick: ["tip"] });
  });

  it("keeps a kind it was told about that the console does not know", () => {
    const next = setKind({ twitch: ["future"] }, "twitch", "tip", true);
    expect(next.twitch).toEqual(["tip", "future"]);
  });

  it("goes back to defaults by REMOVING the key", () => {
    const start = { twitch: ["tip"], kick: [] as string[] };
    const next = resetPlatform(start, "twitch");
    expect(Object.hasOwn(next, "twitch")).toBe(false);
    expect(next).toEqual({ kick: [] });
    expect(isCustomised(start, "twitch")).toBe(true);
    expect(isCustomised(next, "twitch")).toBe(false);
    expect(isCustomised(undefined, "twitch")).toBe(false);
    expect(start.twitch).toEqual(["tip"]);
  });
});

describe("minimum amounts", () => {
  it("accepts money XOR unit, with the matching fields", () => {
    expect(minProblem({ currency: "USD", minor: 500 })).toBeNull();
    expect(minProblem({ currency: "usd", minor: 0 })).toBeNull();
    expect(minProblem({ unit: "bits", units: 100 })).toBeNull();
    expect(minProblem({ currency: "US", minor: 5 })).toBe("currency");
    expect(minProblem({ currency: "USD" })).toBe("incomplete");
    expect(minProblem({ unit: "b1ts", units: 5 })).toBe("unit");
    expect(minProblem({ unit: "bits" })).toBe("incomplete");
    expect(minProblem({ currency: "USD", minor: 5, unit: "bits", units: 1 })).toBe("both");
    expect(minProblem({})).toBe("incomplete");
    expect(minProblem({ currency: "USD", minor: -1 })).toBe("incomplete");
  });

  it("normalises on save: case, integers, empty rows dropped", () => {
    expect(
      normaliseMinTip([
        { currency: " usd ", minor: 499.6 },
        { unit: "BITS", units: 100 },
        {},
      ]),
    ).toEqual([
      { currency: "USD", minor: 500 },
      { unit: "bits", units: 100 },
    ]);
    expect(normaliseMinTip(undefined)).toEqual([]);
  });

  it("names the first problem row", () => {
    expect(firstMinProblem([{ currency: "USD", minor: 1 }, { unit: "9", units: 1 }])).toEqual({
      index: 1,
      problem: "unit",
    });
    expect(firstMinProblem([{ currency: "USD", minor: 1 }])).toBeNull();
    expect(firstMinProblem(undefined)).toBeNull();
  });
});

describe("activityForSave and sameActivity", () => {
  const base = { enabled: true, overlayMessages: true, reactions: false };

  it("sends an empty kinds map when the last custom platform was reset", () => {
    const stored = { ...base, kinds: { twitch: ["tip"] } };
    expect(activityForSave({ ...base, kinds: {} }, stored).kinds).toEqual({});
    expect(activityForSave({ ...base }, stored).kinds).toEqual({});
  });

  it("omits kinds and minTip when there never were any", () => {
    const out = activityForSave({ ...base, kinds: {}, minTip: [] }, base);
    expect("kinds" in out).toBe(false);
    expect("minTip" in out).toBe(false);
  });

  it("clears minTip with [] when rows were removed", () => {
    expect(activityForSave(base, { ...base, minTip: [{ unit: "bits", units: 5 }] }).minTip).toEqual([]);
  });

  it("normalises the rows it sends", () => {
    expect(activityForSave({ ...base, minTip: [{ currency: "eur", minor: 100 }] }, undefined).minTip).toEqual([
      { currency: "EUR", minor: 100 },
    ]);
  });

  it("compares independent of key order", () => {
    expect(sameActivity({ ...base, kinds: { a: ["x"], b: [] } }, { ...base, kinds: { b: [], a: ["x"] } })).toBe(true);
    expect(sameActivity(base, { ...base, reactions: true })).toBe(false);
    expect(sameActivity(base, { ...base, kinds: {} })).toBe(true);
  });
});

describe("overlay kinds", () => {
  it("are the nine the overlay draws", () => {
    expect(ACTIVITY_KINDS.filter(isOverlayKind)).toHaveLength(9);
    expect(isOverlayKind("poll")).toBe(false);
    expect(isOverlayKind("tip")).toBe(true);
  });
});
