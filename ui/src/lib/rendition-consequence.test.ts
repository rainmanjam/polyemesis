import { describe, expect, it } from "vitest";
import {
  computeLeaving,
  joinConsequence,
  leaveConsequence,
  type RenditionUsage,
} from "./rendition-consequence";

/** The four states of the consequence line.
 *
 *  Two of them — what happens to the encode you are LEAVING — are the novel
 *  part of this design; nothing surveyed in docs/notes/video-treatment-ui.md
 *  tells an operator that at all. They are also the two that were untested,
 *  which is how "N other destinations stay on it" shipped without anyone
 *  confirming N is right.
 */

const usage = (destinations: number, enabledDestinations: number): RenditionUsage => ({
  destinations,
  enabledDestinations,
});

describe("joinConsequence", () => {
  it("says joining is free when the encode is already running", () => {
    const line = joinConsequence(usage(3, 2));
    expect(line).toContain("already encoding");
    expect(line).toContain("no new encode starts");
    // The count shown is total destinations, not enabled ones: the operator is
    // being told how many things share this picture, not how many are live.
    expect(line).toContain("3 destinations");
  });

  it("says an idle encode will START, which is the expensive case", () => {
    const line = joinConsequence(usage(4, 0));
    expect(line).toBe("Starts one shared encode when an enabled destination uses it.");
    // The free-case wording must not leak into the paid case. These are the two
    // sentences the whole shared model turns on.
    expect(line).not.toContain("no new encode starts");
  });

  it("does not claim an encode is running just because destinations exist", () => {
    // Four destinations configured, none enabled: nothing is encoding. A check
    // on `destinations` rather than `enabledDestinations` gets this backwards
    // and promises a free join that will actually spawn a process.
    expect(joinConsequence(usage(4, 0))).toContain("Starts one");
  });

  it("counts one destination in the singular", () => {
    expect(joinConsequence(usage(1, 1))).toContain("Feeds 1 destination ·");
    expect(joinConsequence(usage(1, 1))).not.toContain("1 destinations");
  });
});

describe("leaveConsequence", () => {
  it("warns that the encode stops when this was the last one on it", () => {
    const line = leaveConsequence({ name: "720p30 backup", lastOut: true, others: 0 });
    expect(line).toContain("Stops the");
    expect(line).toContain("720p30 backup");
  });

  it("reassures when others remain, and counts them", () => {
    const line = leaveConsequence({ name: "1080p60", lastOut: false, others: 2 });
    expect(line).toBe("2 other enabled destinations stay on “1080p60”. Nothing else changes.");
    expect(line).not.toContain("Stops");
  });

  it("uses the singular for a single survivor", () => {
    const line = leaveConsequence({ name: "1080p60", lastOut: false, others: 1 });
    expect(line).toContain("1 other enabled destination stay");
    expect(line).not.toContain("1 other enabled destinations");
  });
});

describe("computeLeaving", () => {
  const catalogue = {
    10: { name: "1080p60", destinations: 3, enabledDestinations: 3 },
    11: { name: "720p30 backup", destinations: 1, enabledDestinations: 1 },
    12: { name: "idle tier", destinations: 2, enabledDestinations: 0 },
  } as Record<number, RenditionUsage & { name: string }>;
  const usageOf = (id: number) => catalogue[id] ?? null;

  it("says nothing when the destination had no encode to begin with", () => {
    expect(computeLeaving(null, "10", true, usageOf)).toBeNull();
    expect(computeLeaving(undefined, "10", true, usageOf)).toBeNull();
    // Rendition ids are truthy positives; 0 is not a real id and must not be
    // treated as "had one".
    expect(computeLeaving(0, "10", true, usageOf)).toBeNull();
  });

  it("says nothing when the selection has not moved", () => {
    // The dialog re-renders constantly; a leave warning for the encode you are
    // still on is noise that teaches operators to ignore the line.
    expect(computeLeaving(10, "10", true, usageOf)).toBeNull();
  });

  it("says nothing about an encode it cannot find", () => {
    // Better silent than confidently wrong about someone else's stream.
    expect(computeLeaving(99, "10", true, usageOf)).toBeNull();
  });

  it("reports last-one-out when this destination is the only enabled user", () => {
    // enabledDestinations is 1 — and that 1 IS this destination, because
    // nothing has been saved yet. Reaching zero would mean it had already been
    // removed, which never happens while the dialog is open.
    const leaving = computeLeaving(11, "", true, usageOf);
    expect(leaving).toEqual({ name: "720p30 backup", lastOut: true, others: 0 });
  });

  it("does not report last-one-out while others are still enabled", () => {
    const leaving = computeLeaving(10, "", true, usageOf);
    expect(leaving?.lastOut).toBe(false);
    // Three enabled, minus this one, leaves two.
    expect(leaving?.others).toBe(2);
  });

  it("never claims a DISABLED destination's departure stops an encode", () => {
    // A disabled destination was never in the enabled count, so removing it
    // changes nothing. Warning that an encode will stop when it will not is
    // the failure that makes the whole line untrustworthy.
    const leaving = computeLeaving(11, "", false, usageOf);
    expect(leaving?.lastOut).toBe(false);
  });

  it("does not report a negative survivor count for an idle encode", () => {
    // Zero enabled destinations, so 0 - 1 = -1 without the clamp: "-1 other
    // enabled destinations stay on it".
    const leaving = computeLeaving(12, "", true, usageOf);
    expect(leaving?.others).toBe(0);
    expect(leaveConsequence(leaving!)).not.toContain("-1");
  });

  it("fires when moving to copy, not just between encodes", () => {
    // Switching to passthrough is the selection most likely to strand an
    // encode, and its form value is the empty string rather than an id.
    expect(computeLeaving(11, "", true, usageOf)).not.toBeNull();
  });
});
