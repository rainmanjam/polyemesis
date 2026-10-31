import { describe, expect, it } from "vitest";
import { healthVerdict } from "./healthVerdict";
import type { AttentionItem } from "./attention";
import type { DestinationId } from "./types";

const air = (over: Partial<{ live: number; enabled: number; degraded: number; failed: number }> = {}) => ({
  live: 0,
  enabled: 0,
  degraded: 0,
  failed: 0,
  ...over,
});

const did = (n: number) => n as unknown as DestinationId;
const destItem = (id: string): AttentionItem => ({
  key: `dest:${id}`,
  tone: "down",
  subject: id,
  destinationId: did(id.length),
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
    expect(v).toEqual({ kind: "attention", count: 1, total: 5, firstDestinationId: did(6) });
  });

  it("counts a destination once even when it has more than one problem row", () => {
    const v = healthVerdict(
      air({ live: 3, enabled: 5, failed: 2 }),
      [destItem("a"), { ...destItem("a"), key: "dest:a:2" }, destItem("bb")],
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

  it("gives no healthy verdict while the broadcast is on a backup or a slate", () => {
    // Every destination is running and fed by the slate. The verdict must not
    // say "All healthy" directly above a red "Slate on air" banner.
    expect(healthVerdict(air({ live: 5, enabled: 5 }), [], 760, true)).toBeNull();
  });

  it("reports uptime as null when the ingest has none to report (SRT, #514)", () => {
    expect(healthVerdict(air({ live: 2, enabled: 2 }), [], null)).toEqual({
      kind: "healthy",
      count: 2,
      uptimeSec: null,
    });
  });

  // A3. The verdict used to read only the destinations and the attention rows.
  // An ingest that was not receiving -- the header chip's own test for it,
  // which is bytes arriving rather than a process existing -- was invisible
  // here, so the strip said "All healthy" over a dead feed.
  it("does not claim healthy while the ingest is not receiving, even with every destination running", () => {
    expect(healthVerdict(air({ live: 5, enabled: 5 }), [], 760, false, false)).toEqual({
      kind: "no-ingest",
    });
  });

  it("still says healthy when the ingest is receiving, and when the signal is not known", () => {
    // Undefined is not false: a page that has not read the ingest yet must not
    // invent a fault, and an SRT source is receiving with no ingest process.
    expect(healthVerdict(air({ live: 5, enabled: 5 }), [], 760, false, true)?.kind).toBe("healthy");
    expect(healthVerdict(air({ live: 5, enabled: 5 }), [], 760)?.kind).toBe("healthy");
  });

  it("names the destination that needs attention ahead of the missing feed", () => {
    // A destination that is failing is the more specific thing to act on.
    const v = healthVerdict(air({ live: 4, enabled: 5, failed: 1 }), [destItem("yt")], null, false, false);
    expect(v?.kind).toBe("attention");
  });
});
