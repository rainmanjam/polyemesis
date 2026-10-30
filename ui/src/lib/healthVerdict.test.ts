import { describe, expect, it } from "vitest";
import { healthVerdict } from "./healthVerdict";
import type { AttentionItem } from "./attention";

const air = (over: Partial<{ live: number; enabled: number; degraded: number; failed: number }> = {}) => ({
  live: 0,
  enabled: 0,
  degraded: 0,
  failed: 0,
  ...over,
});

const destItem = (id: string): AttentionItem => ({
  key: `dest:${id}`,
  tone: "down",
  subject: id,
  destinationId: id,
} as AttentionItem);

describe("healthVerdict", () => {
  it("is healthy when every enabled destination is live and nothing needs attention", () => {
    expect(healthVerdict(air({ live: 5, enabled: 5 }), [], 760)).toEqual({
      kind: "healthy",
      count: 5,
      uptimeSec: 760,
    });
  });

  it("says how many destinations need attention, out of how many there are", () => {
    const v = healthVerdict(air({ live: 4, enabled: 5, failed: 1 }), [destItem("twitch")], 760);
    expect(v).toEqual({ kind: "attention", count: 1, total: 5, firstDestinationId: "twitch" });
  });

  it("counts a destination once even when it has more than one problem row", () => {
    const v = healthVerdict(
      air({ live: 3, enabled: 5, failed: 2 }),
      [destItem("a"), { ...destItem("a"), key: "dest:a:2" }, destItem("b")],
      0,
    );
    expect(v).toMatchObject({ kind: "attention", count: 2, total: 5 });
  });

  it("never claims healthy while an enabled destination is not live", () => {
    // Five enabled, four live, and the fifth is quietly stopped with no error.
    // "All healthy" here would be a lie about the one that is not on air.
    expect(healthVerdict(air({ live: 4, enabled: 5 }), [], 760)).toBeNull();
  });

  it("gives no verdict for an install with nothing enabled", () => {
    expect(healthVerdict(air(), [], null)).toBeNull();
  });

  it("gives no verdict when the only problems are not about a destination", () => {
    // Ingest rows have no card to scroll to. The list below the strip says
    // what they are; a verdict naming a destination count would be wrong.
    const ingest: AttentionItem = { key: "ingest", tone: "down", subject: "Ingest" } as AttentionItem;
    expect(healthVerdict(air({ live: 5, enabled: 5 }), [ingest], 760)).toBeNull();
  });

  it("reports uptime as null when the ingest has none to report (SRT, #514)", () => {
    expect(healthVerdict(air({ live: 2, enabled: 2 }), [], null)).toEqual({
      kind: "healthy",
      count: 2,
      uptimeSec: null,
    });
  });
});
