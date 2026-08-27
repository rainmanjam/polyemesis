import { describe, expect, it } from "vitest";

import { topRowLayout } from "@/lib/dashboardLayout";

/** How many columns a Tailwind `lg:grid-cols-[a_b_c]` template declares.
 *
 *  Counted from the string rather than trusted from the `columns` field, so
 *  the assertion below is about the class the browser receives and not about a
 *  number sitting next to it. A template edited to add a column without
 *  touching `columns` is exactly the drift this file exists to catch. */
function declaredColumns(gridClass: string): number {
  const inner = gridClass.match(/grid-cols-\[(.+)\]/)?.[1];
  if (!inner) throw new Error(`no bracketed grid template in ${gridClass}`);
  // Split on underscores that are not inside parentheses: minmax(0,1fr) is one
  // column, and it contains no underscore, but a future clamp() might.
  let depth = 0;
  let count = 1;
  for (const ch of inner) {
    if (ch === "(") depth++;
    else if (ch === ")") depth--;
    else if (ch === "_" && depth === 0) count++;
  }
  return count;
}

describe("topRowLayout", () => {
  it("gives the side cards a column each when lanes suppress the preview", () => {
    const laned = topRowLayout(true);
    expect(laned.sideStacked).toBe(false);
    expect(declaredColumns(laned.gridClass)).toBe(3);
  });

  it("stacks the side cards beside the preview when there is one", () => {
    const plain = topRowLayout(false);
    expect(plain.sideStacked).toBe(true);
    expect(declaredColumns(plain.gridClass)).toBe(2);
  });

  /* THE INVARIANT, and the reason both facts come from one call.
   *
   * Three columns with the cards still stacked leaves an empty third column.
   * Two columns with them unstacked pushes the pipeline out of the grid. Either
   * mistake renders as a layout that looks deliberate, which is how the
   * original ~400px void in #614 survived long enough to be photographed. */
  it("keeps the column count and the stacking describing the same layout", () => {
    for (const laned of [true, false]) {
      const l = topRowLayout(laned);
      expect(declaredColumns(l.gridClass)).toBe(l.columns);
      // One cell for the preview/ingest column, plus one per side card when
      // they are not stacked, plus one shared cell when they are.
      const sideCells = l.sideStacked ? 1 : 2;
      expect(l.columns).toBe(1 + sideCells);
    }
  });

  it("always leaves the first column flexible, so the ingest card can shrink", () => {
    // A fixed first column would overflow the row on a narrow desktop; every
    // other column here is a fixed 20rem.
    for (const laned of [true, false]) {
      expect(topRowLayout(laned).gridClass).toContain("minmax(0,1fr)");
    }
  });
});
