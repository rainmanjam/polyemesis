import { describe, expect, it } from "vitest";

import { STORIES } from "./stories";
import { VIEWBOX, anchors, linesFor, type Layout } from "./geometry";

const LAYOUTS: readonly Layout[] = ["h", "v"];

describe("split diagram geometry", () => {
  it("emits 5 source lines plus a video and an audio line per destination with video", () => {
    expect(linesFor(STORIES.streamer, "h")).toHaveLength(5 + 4 + 4);
    expect(linesFor(STORIES.worship, "h")).toHaveLength(5 + 3 + 4); // audio-only feed has no video line
  });

  it.each(["h", "v"] as const)("starts and ends every %s line on an anchor", (layout) => {
    const a = anchors(layout, 4);
    for (const l of linesFor(STORIES.sports, layout)) {
      const [sx, sy] = l.d.match(/^M\s*([\d.]+)[ ,]([\d.]+)/)!.slice(1).map(Number);
      const [ex, ey] = l.d.match(/([\d.]+)[ ,]([\d.]+)\s*$/)!.slice(1).map(Number);
      const start = l.from === "hub" ? a.hub : a.src[l.from as number];
      const end = l.to === "hub" ? a.hub : a.dst[STORIES.sports.dests.findIndex((d) => d.id === l.to)];
      expect([sx, sy]).toEqual([start.x, start.y]);
      expect([ex, ey]).toEqual([end.x, end.y]);
    }
  });

  it("keeps anchors inside the viewBox, rounded to one decimal", () => {
    for (const layout of LAYOUTS) {
      const { w, h } = VIEWBOX[layout];
      const a = anchors(layout, 4);
      expect(a.src).toHaveLength(5);
      expect(a.dst).toHaveLength(4);
      for (const p of [...a.src, a.hub, ...a.dst]) {
        expect(p.x).toBeGreaterThanOrEqual(0);
        expect(p.x).toBeLessThanOrEqual(w);
        expect(p.y).toBeGreaterThanOrEqual(0);
        expect(p.y).toBeLessThanOrEqual(h);
        expect(Math.round(p.x * 10) / 10).toBe(p.x);
        expect(Math.round(p.y * 10) / 10).toBe(p.y);
      }
    }
  });

  it("pins the horizontal anchors to the grid's column centres", () => {
    const a = anchors("h", 4);
    expect(a.src.map((p) => p.x)).toEqual([90, 90, 90, 90, 90]);
    expect(a.src.map((p) => p.y)).toEqual([36, 108, 180, 252, 324]);
    expect(a.hub).toEqual({ x: 500, y: 180 });
    expect(a.dst.map((p) => [p.x, p.y])).toEqual([[910, 45], [910, 135], [910, 225], [910, 315]]);
  });

  it("draws each segment as one cubic Bezier with 1-decimal coordinates", () => {
    for (const layout of LAYOUTS) {
      for (const l of linesFor(STORIES.streamer, layout)) {
        expect(l.d).toMatch(/^M\s?-?\d+(\.\d)? -?\d+(\.\d)? C(\s?-?\d+(\.\d)? -?\d+(\.\d)?,?){3}$/);
      }
    }
  });

  it("tags each line with the step table's data-on", () => {
    const ls = linesFor(STORIES.streamer, "h");
    expect(ls.find((l) => l.kind === "video" && l.to === "hub")!.on).toBe("1 2 5");
    expect(ls.filter((l) => l.kind === "audio" && l.from === "hub").every((l) => l.on === "4 5")).toBe(true);
    expect(ls.filter((l) => l.kind === "audio" && l.to === "hub").every((l) => l.on === "1 3 4 5")).toBe(true);
    expect(ls.filter((l) => l.kind === "video" && l.from === "hub").every((l) => l.on === "2 5")).toBe(true);
  });
});
