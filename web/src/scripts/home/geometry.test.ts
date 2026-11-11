import { describe, expect, it } from "vitest";

import { STORIES, STORY_ORDER } from "./stories";
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

  /* SplitDiagram's CSS hard-codes four destination rows (h) and a 2 x 2 grid (v). */
  it.each(STORY_ORDER)("gives the %s story exactly four destinations", (key) => {
    expect(STORIES[key].dests).toHaveLength(4);
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

  it("routes the v layout's second-row destinations down the seam, not across the first row", () => {
    const a = anchors("v", 4);
    const lower = new Set(STORIES.streamer.dests.slice(2).map((d) => d.id));
    const lines = linesFor(STORIES.streamer, "v").filter((l) => lower.has(l.to));
    expect(lines.length).toBeGreaterThanOrEqual(4);
    for (const l of lines) {
      const nums = l.d.match(/-?\d+(\.\d)?/g)!.map(Number);
      // M a C c1, c2, end: both control points stay within a few units of the hub's column.
      for (const cx of [nums[2], nums[4]]) expect(Math.abs(cx - a.hub.x)).toBeLessThanOrEqual(2);
    }
  });

  it("puts each second-row line on its own x along the seam: video toward its card, audio away", () => {
    const a = anchors("v", 4);
    const lower = STORIES.streamer.dests.slice(2);
    const seamX = new Map<string, number>();
    for (const l of linesFor(STORIES.streamer, "v")) {
      const j = lower.findIndex((d) => d.id === l.to);
      if (j < 0 || l.from !== "hub") continue;
      const nums = l.d.match(/-?\d+(\.\d)?/g)!.map(Number);
      const [c1x, c1y, c2x, c2y, ex, ey] = nums.slice(2);
      const card = a.dst[2 + j];
      // ends on the card, arriving level with it (hooked in from the side)
      expect([ex, ey]).toEqual([card.x, card.y]);
      expect(c1y).toBe(card.y);
      expect(c2y).toBe(card.y);
      expect(c1x).toBe(c2x);
      const toward = Math.sign(card.x - a.hub.x);
      expect(Math.sign(c1x - a.hub.x)).toBe(l.kind === "video" ? toward : -toward);
      seamX.set(`${l.to}/${l.kind}`, c1x);
    }
    const xs = [...seamX.values()];
    expect(xs.length).toBeGreaterThanOrEqual(3);
    expect(new Set(xs).size).toBe(xs.length);
  });

  it("leaves the h layout on plain curves: level start and end tangents, no seam", () => {
    const a = anchors("h", 4);
    for (const story of STORY_ORDER) {
      for (const l of linesFor(STORIES[story], "h")) {
        const [sx, sy, c1x, c1y, c2x, c2y, ex, ey] = l.d.match(/-?\d+(\.\d)?/g)!.map(Number);
        expect(c1y).toBe(sy);
        expect(c2y).toBe(ey);
        expect(c1x).toBeGreaterThan(sx);
        expect(c2x).toBeLessThan(ex);
        if (l.from === "hub") expect([sx, sy]).toEqual([a.hub.x, a.hub.y]);
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
