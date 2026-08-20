import { describe, expect, it } from "vitest";

import { laneLayout } from "./sourceLanes";

const src = (id: number, name: string) => ({ id, name }) as never;
const dst = (id: number, name: string, sourceId?: number | null) =>
  ({ id, name, sourceId }) as never;
const tile = (id: number, name: string, over: Record<string, unknown> = {}) =>
  ({ id, name, outputLive: true, ingestLive: true, ...over }) as never;

const two = [src(1, "Main"), src(2, "Studio B")];

describe("laneLayout", () => {
  it("draws no lanes with one programme", () => {
    // A lane around the only programme is a box drawn around the whole page.
    const got = laneLayout([tile(1, "Main")], [dst(1, "Twitch", 1)], [src(1, "Main")]);
    expect(got.laned).toBe(false);
    expect(got.lanes).toEqual([]);
  });

  it("draws no lanes on an install with no programmes", () => {
    expect(laneLayout([], [], []).laned).toBe(false);
  });

  it("puts a programme's preview and its destinations in the same lane", () => {
    // THE WHOLE POINT. "Is this one on air, and where is it going?" is answered
    // by position rather than by reading a name in two places and trusting they
    // match.
    const got = laneLayout(
      [tile(1, "Main"), tile(2, "Studio B")],
      [dst(1, "Twitch", 1), dst(2, "YouTube", 2), dst(3, "Backup", 1)],
      two,
    );
    expect(got.laned).toBe(true);
    expect(got.lanes.map((l) => l.name)).toEqual(["Main", "Studio B"]);
    expect(got.lanes[0].pane?.id).toBe(1);
    expect(got.lanes[0].destinations.map((d) => d.name)).toEqual(["Twitch", "Backup"]);
    expect(got.lanes[1].pane?.id).toBe(2);
    expect(got.lanes[1].destinations.map((d) => d.name)).toEqual(["YouTube"]);
  });

  it("never pairs a lane with another programme's preview", () => {
    // The failure this whole change exists to make impossible. If a lane could
    // show one programme's picture over another's destinations, position would
    // be a lie and worse than the label it replaced.
    const got = laneLayout([tile(2, "Studio B")], [dst(1, "Twitch", 1)], two);
    const main = got.lanes.find((l) => l.sourceId === 1);
    expect(main?.pane).toBeUndefined();
    expect(got.lanes.find((l) => l.sourceId === 2)?.pane?.id).toBe(2);
    for (const l of got.lanes) if (l.pane) expect(l.pane.id).toBe(l.sourceId);
  });

  it("keeps a lane for a programme with no telemetry yet", () => {
    // The lane is the programme, not the picture. Hiding it until a tile
    // arrives would make a source that has never been streamed to look like a
    // source that does not exist.
    const got = laneLayout([], [dst(1, "Twitch", 1)], two);
    expect(got.lanes.map((l) => l.name)).toEqual(["Main", "Studio B"]);
    expect(got.lanes.every((l) => l.pane === undefined)).toBe(true);
  });

  it("keeps a lane for a programme with no destinations", () => {
    const got = laneLayout([tile(1, "Main"), tile(2, "Studio B")], [dst(1, "Twitch", 1)], two);
    expect(got.lanes[1].destinations).toEqual([]);
    expect(got.lanes[1].pane?.id).toBe(2);
  });

  it("hands orphans back rather than dropping them", () => {
    // Inherited from destinationLayout rather than restated, which is the point
    // of composing: this is the rule whose drift loses a card.
    const got = laneLayout([], [dst(1, "Twitch", 1), dst(9, "stray", 99)], two);
    expect(got.orphans.map((d) => d.name)).toEqual(["stray"]);
    const inLanes = got.lanes.flatMap((l) => l.destinations.map((d) => d.name));
    expect(inLanes).not.toContain("stray");
    expect([...inLanes, ...got.orphans.map((d) => d.name)]).toHaveLength(2);
  });

  it("reports no orphans when there are none", () => {
    expect(laneLayout([], [dst(1, "Twitch", 1)], two).orphans).toEqual([]);
  });

  it("follows the source list's order", () => {
    const got = laneLayout([], [], [src(2, "Zebra"), src(1, "Alpha")]);
    expect(got.lanes.map((l) => l.name)).toEqual(["Zebra", "Alpha"]);
  });
});
