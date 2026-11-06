import { describe, expect, it } from "vitest";

import { destinationLayout, hasHeading } from "./destinationGroups";

const src = (id: number, name: string) => ({ id, name }) as never;
const dst = (id: number, name: string, sourceId?: number | null) =>
  ({ id, name, sourceId }) as never;

const two = [src(1, "Main"), src(2, "Studio B")];

describe("destinationLayout", () => {
  it("draws no headings when there is only one programme", () => {
    // A heading repeated over every card distinguishes nothing and costs a line
    // of the screen an operator watches while live.
    const got = destinationLayout([dst(1, "Twitch", 1), dst(2, "YouTube", 1)], [src(1, "Main")]);
    expect(got.grouped).toBe(false);
    expect(got.groups).toHaveLength(1);
    expect(got.groups[0].destinations.map((d) => d.name)).toEqual(["Twitch", "YouTube"]);
  });

  it("draws no headings on an install with no programmes at all", () => {
    const got = destinationLayout([], []);
    expect(got.grouped).toBe(false);
  });

  it("splits by programme once there is more than one", () => {
    const got = destinationLayout(
      [dst(1, "Twitch", 1), dst(2, "YouTube", 2), dst(3, "Backup", 1)],
      two,
    );
    expect(got.grouped).toBe(true);
    expect(got.groups.map((g) => g.name)).toEqual(["Main", "Studio B"]);
    expect(got.groups[0].destinations.map((d) => d.name)).toEqual(["Twitch", "Backup"]);
    expect(got.groups[1].destinations.map((d) => d.name)).toEqual(["YouTube"]);
  });

  it("keeps the caller's order inside a group", () => {
    // The dashboard lets an operator move destinations by hand and persists
    // that order. Re-sorting here would silently undo it.
    const got = destinationLayout([dst(3, "C", 1), dst(1, "A", 1), dst(2, "B", 1)], two);
    expect(got.groups[0].destinations.map((d) => d.name)).toEqual(["C", "A", "B"]);
  });

  it("follows the source list's order, not the alphabet", () => {
    // So the dashboard reads in the same order as the Sources page.
    const got = destinationLayout([dst(1, "x", 2), dst(2, "y", 1)], [src(2, "Zebra"), src(1, "Alpha")]);
    expect(got.groups.map((g) => g.name)).toEqual(["Zebra", "Alpha"]);
  });

  it("keeps the heading of a programme that has no destinations", () => {
    // The state an operator has to notice in order to fix. Hiding the heading
    // would make "nothing configured yet" look like "no such programme".
    const got = destinationLayout([dst(1, "Twitch", 1)], two);
    expect(got.groups.map((g) => g.name)).toEqual(["Main", "Studio B"]);
    expect(got.groups[1].destinations).toEqual([]);
  });

  describe("a destination whose programme is not in the list", () => {
    // THE ONE THAT MATTERS. A source deleted in another tab, a status snapshot
    // that arrived before the source list, a row whose source_id survived a
    // restore its programme did not. Filtering it out would remove a
    // destination that is still configured and may still be running, from the
    // only screen that lists it.
    const stray = [dst(1, "Twitch", 1), dst(9, "orphaned", 99), dst(8, "no source at all")];

    it("is kept, not dropped", () => {
      const got = destinationLayout(stray, two);
      const all = got.groups.flatMap((g) => g.destinations.map((d) => d.name));
      expect(all).toContain("orphaned");
      expect(all).toContain("no source at all");
      expect(all).toHaveLength(3);
    });

    it("is flagged so the caller can say something is wrong", () => {
      const got = destinationLayout(stray, two);
      const last = got.groups[got.groups.length - 1];
      expect(last.orphans).toBe(true);
      expect(last.destinations.map((d) => d.name)).toEqual(["orphaned", "no source at all"]);
    });

    it("gets no ordinary heading, because it is a problem rather than a programme", () => {
      const got = destinationLayout(stray, two);
      const last = got.groups[got.groups.length - 1];
      expect(hasHeading(got, last)).toBe(false);
      expect(hasHeading(got, got.groups[0])).toBe(true);
    });

    it("produces no orphan group when there are none", () => {
      const got = destinationLayout([dst(1, "Twitch", 1)], two);
      expect(got.groups.some((g) => g.orphans)).toBe(false);
    });
  });

  it("does not group a single-programme install even when a destination is orphaned", () => {
    // With one programme there are no headings to hang an orphan under, and the
    // card must still be drawn.
    const got = destinationLayout([dst(1, "Twitch", 77)], [src(1, "Main")]);
    expect(got.grouped).toBe(false);
    expect(got.groups[0].destinations.map((d) => d.name)).toEqual(["Twitch"]);
  });
});
