import { describe, expect, it } from "vitest";

import { previewLayout } from "./previewLayout";
import type { PreviewTile } from "@/hooks/usePreviewTiles";

/* THE DECISION BEHIND THE PREVIEW GRID, TESTED AWAY FROM THE PAGE.
 *
 * This used to be ten branches of JSX inside Dashboard.tsx, where the only way
 * to reach it was to render the whole dashboard. It is a plain function of the
 * polled telemetry, so it is tested as one.
 *
 * The bug class this feature exists to fix is a tile showing the WRONG
 * programme -- the preview was a single player pinned to whichever source came
 * first in display order, and switching sources left the previous picture under
 * the new one's name. So the assertions here are mostly about pairing: the id a
 * pane plays, the name it is captioned with and the geometry it is drawn at all
 * having to come from the same source.
 */

const src = (over: Partial<PreviewTile> & { id: number }): PreviewTile => ({
  name: `source ${over.id}`,
  outputLive: false,
  ingestLive: false,
  ...over,
});

describe("previewLayout", () => {
  it("gives every source its own tile, playing its own programme", () => {
    const { grid, panes } = previewLayout([
      src({ id: 7, name: "Studio A", outputLive: true, ingestLive: true }),
      src({ id: 9, name: "Studio B", onAir: "slate" }),
    ]);

    expect(grid, "two sources still drew one player").toBe(true);
    expect(panes.map((p) => [p.label, p.player.sourceId])).toEqual([
      ["Studio A", 7],
      ["Studio B", 9],
    ]);
  });

  it("carries each source's own on-air state onto its own tile", () => {
    // The state that decides whether a tile shows a picture at all, and what it
    // says over it. Reading it off the wrong source is how a dead programme
    // borrows a live one's frame.
    const [a, b] = previewLayout([
      src({ id: 1, outputLive: true, ingestLive: false, onAir: "backup" }),
      src({ id: 2, outputLive: false, ingestLive: false }),
    ]).panes;

    expect(a.player).toMatchObject({
      outputLive: true,
      ingestLive: false,
      onAir: "backup",
    });
    expect(b.player).toMatchObject({ outputLive: false, ingestLive: false });
    expect(b.player.onAir).toBeUndefined();
  });

  it("draws one source full width rather than in a half-width grid", () => {
    const { grid, panes } = previewLayout([src({ id: 4, name: "Only" })]);
    expect(grid).toBe(false);
    expect(panes).toHaveLength(1);
    // Still qualified by its id: the tile has to load that source's playlist,
    // not the default alias, or a single-source install with a non-default
    // source watches nothing.
    expect(panes[0].player.sourceId).toBe(4);
    expect(panes[0].label).toBe("Only");
  });

  it("still offers a preview before any telemetry has arrived", () => {
    // Empty is the state of EVERY load until the first /previews answers, and
    // the permanent state of an install whose poll keeps failing. Returning no
    // panes would blank the dashboard's main picture in both cases.
    const { grid, panes } = previewLayout([]);
    expect(grid).toBe(false);
    expect(panes).toHaveLength(1);
    // Nothing set, which is the unqualified player that predates this feature:
    // it falls back to the default source's alias.
    expect(panes[0].player).toEqual({
      sourceId: undefined,
      outputLive: undefined,
      ingestLive: undefined,
      onAir: undefined,
      aspect: undefined,
    });
  });

  it("passes a measured geometry through so the tile is not letterboxed", () => {
    const vertical = previewLayout([
      src({ id: 1, width: 1080, height: 1920 }),
      src({ id: 2, width: 1920, height: 1080 }),
    ]).panes;
    expect(vertical[0].player.aspect).toEqual({ width: 1080, height: 1920 });
    expect(vertical[1].player.aspect).toEqual({ width: 1920, height: 1080 });
  });

  it("withholds a half-measured geometry rather than collapsing the tile", () => {
    // A zero or a missing half would render as `aspect-ratio: 1080 / 0`, which
    // is a tile with no height. Undefined lets the player fall back to 16:9.
    for (const partial of [
      { width: 1920 },
      { height: 1080 },
      { width: 0, height: 1080 },
      { width: 1920, height: 0 },
      {},
    ]) {
      expect(
        previewLayout([src({ id: 1, ...partial })]).panes[0].player.aspect,
        `${JSON.stringify(partial)} produced an aspect ratio`,
      ).toBeUndefined();
      expect(
        previewLayout([src({ id: 1, ...partial }), src({ id: 2 })]).panes[0]
          .player.aspect,
        `${JSON.stringify(partial)} produced an aspect ratio in the grid`,
      ).toBeUndefined();
    }
  });

  it("keys every tile by the source it shows", () => {
    // The value Dashboard keys the list on. Keying by array index instead
    // would let React reuse a mounted <video> for a different source when one
    // is added or removed -- the stale-picture bug again, arriving through the
    // reconciler -- so a pane has to carry an id that is stable per source and
    // is not its position.
    //
    // WHAT THIS DOES NOT REACH: whether Dashboard actually passes it to `key`.
    // That lives in JSX and would need a page render to pin; here the contract
    // is only that the id is available and is the source's own.
    const panes = previewLayout([
      src({ id: 12 }),
      src({ id: 3 }),
      src({ id: 40 }),
    ]).panes;
    expect(panes.map((p) => p.id)).toEqual([12, 3, 40]);
  });
});
