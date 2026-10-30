import { describe, expect, it, vi } from "vitest";
import {
  goLiveRows,
  planStarts,
  runPacedStarts,
  youtubeBroadcastsAtRisk,
} from "./goLive";
import type { DestStatus } from "./types";

const dest = (over: Record<string, unknown> = {}) =>
  ({
    id: 1,
    name: "Twitch",
    kind: "rtmp",
    platform: "twitch",
    enabled: false,
    summary: "",
    tracks: null,
    filterComplex: "",
    normalization: "off",
    warnings: null,
    sourceId: 1,
    ...over,
  }) as unknown as DestStatus;

const sources = [{ id: 1, name: "Studio A" }] as never[];

describe("goLiveRows", () => {
  it("marks a destination ready when its programme exists on this install", () => {
    const [row] = goLiveRows([dest()], sources);
    expect(row.ready).toBe(true);
    expect(row.sourceName).toBe("Studio A");
  });

  it("blocks a destination whose programme is gone, and names why", () => {
    // An orphan starts nothing: the engine has no programme to feed it. The
    // sheet must say so rather than offer a Start that will be refused.
    const [row] = goLiveRows([dest({ sourceId: 9 })], sources);
    expect(row.ready).toBe(false);
    expect(row.blockedReason).toBe("noSource");
  });

  it("blocks a destination with no programme at all", () => {
    const [row] = goLiveRows([dest({ sourceId: null })], sources);
    expect(row.blockedReason).toBe("noSource");
  });
});

describe("planStarts", () => {
  it("plans only ready destinations that are not already on", () => {
    const rows = goLiveRows(
      [
        dest({ id: 1 }),
        dest({ id: 2, enabled: true }),
        dest({ id: 3, sourceId: 9 }),
      ],
      sources,
    );
    expect(planStarts(rows).map((d) => d.id)).toEqual([1]);
  });
});

describe("youtubeBroadcastsAtRisk", () => {
  it("counts the YouTube broadcasts that a Stop would end", () => {
    // A completed YouTube broadcast cannot return to live (destinations_bulk.go),
    // so this is the number the operator must see before pressing anything.
    const n = youtubeBroadcastsAtRisk([
      dest({ id: 1, platform: "youtube", enabled: true }),
      dest({ id: 2, platform: "youtube", enabled: false }),
      dest({ id: 3, platform: "twitch", enabled: true }),
    ]);
    expect(n).toBe(1);
  });
});

describe("runPacedStarts", () => {
  it("starts each planned destination in order, one call at a time", async () => {
    const order: number[] = [];
    await runPacedStarts([1, 2, 3], async (id) => {
      order.push(id);
    }, 0);
    expect(order).toEqual([1, 2, 3]);
  });

  it("keeps going after one destination refuses, and reports which one", async () => {
    const start = vi.fn(async (id: number) => {
      if (id === 2) throw new Error("ingest refused");
    });
    const errors = await runPacedStarts([1, 2, 3], start, 0);
    expect(start).toHaveBeenCalledTimes(3);
    expect(errors.get(2)).toBe("ingest refused");
    expect(errors.has(1)).toBe(false);
  });
});
