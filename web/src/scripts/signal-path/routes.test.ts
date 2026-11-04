import { describe, expect, it } from "vitest";

import { buildRoutes, LANES, LANE_GAP, MUSIC_LANE, pointAtDistance, type Connection } from "./routes";
import { cssColorToHex } from "./palette";

/* The wire geometry is pure, so it is tested directly: the WebGL layer and the
 * SVG fallback both draw from it, and a route that crosses a card or loses a
 * lane is a defect nobody sees in a unit run of the page. */

const horizontal: Connection[] = [
  { start: { x: 0, y: 100 }, end: { x: 100, y: 100 }, vertical: false },
  { start: { x: 200, y: 100 }, end: { x: 300, y: 20 }, vertical: false },
  { start: { x: 200, y: 100 }, end: { x: 300, y: 100 }, vertical: false },
  { start: { x: 200, y: 100 }, end: { x: 300, y: 180 }, vertical: false },
];
const vertical: Connection[] = horizontal.map((c, i) => ({
  start: { x: 180, y: 100 + i * 10 },
  end: { x: 100 + i * 40, y: 300 + i * 10 },
  vertical: true,
}));

describe("buildRoutes", () => {
  it("draws five lanes (video, mic, music, game, co-host) per connection", () => {
    /* Four audio tracks, matching the patchbay (src/data/tracks.mjs), plus the video. */
    expect(LANES).toEqual(["video", "mic", "music", "game", "cohost"]);
    expect(MUSIC_LANE).toBe(2);
    for (const layout of [horizontal, vertical]) {
      const routes = buildRoutes({ connections: layout });
      expect(routes.map((r) => r.index)).toEqual([0, 1, 2, 3]);
      for (const r of routes) expect(r.lanes).toHaveLength(5);
    }
  });

  it("starts and ends each lane on its port, offset by the lane gap", () => {
    const [first] = buildRoutes({ connections: horizontal });
    first.lanes.forEach((lane, k) => {
      const offset = (k - 2) * LANE_GAP;
      expect(lane.pts[0][0]).toBeCloseTo(0);
      expect(lane.pts[0][1]).toBeCloseTo(100 + offset);
      expect(lane.pts.at(-1)![0]).toBeCloseTo(100);
      expect(lane.pts.at(-1)![1]).toBeCloseTo(100 + offset);
    });
  });

  it("keeps cumulative length monotonic and equal to the last entry", () => {
    for (const layout of [horizontal, vertical]) {
      for (const r of buildRoutes({ connections: layout })) {
        for (const lane of r.lanes) {
          expect(lane.length).toBeGreaterThan(0);
          expect(lane.cum.at(-1)).toBe(lane.length);
          for (let i = 1; i < lane.cum.length; i++) expect(lane.cum[i]).toBeGreaterThanOrEqual(lane.cum[i - 1]);
        }
      }
    }
  });

  it("keeps vertical routes moving down, never back up through a card", () => {
    for (const r of buildRoutes({ connections: vertical })) {
      for (const lane of r.lanes) {
        for (let i = 1; i < lane.pts.length; i++) expect(lane.pts[i][1]).toBeGreaterThanOrEqual(lane.pts[i - 1][1] - 1e-6);
      }
    }
  });
});

describe("pointAtDistance", () => {
  const [route] = buildRoutes({ connections: horizontal });
  const lane = route.lanes[0];

  it("returns the endpoints at 0 and at full length, and clamps outside", () => {
    const out = [0, 0];
    pointAtDistance(lane, 0, out);
    expect(out[0]).toBeCloseTo(lane.pts[0][0]);
    pointAtDistance(lane, lane.length, out);
    expect(out[0]).toBeCloseTo(lane.pts.at(-1)![0]);
    pointAtDistance(lane, -50, out);
    expect(out[0]).toBeCloseTo(lane.pts[0][0]);
    pointAtDistance(lane, lane.length + 50, out);
    expect(out[0]).toBeCloseTo(lane.pts.at(-1)![0]);
  });

  it("returns a unit tangent heading along the wire", () => {
    const out = [0, 0];
    const tan = [0, 0];
    pointAtDistance(lane, lane.length / 2, out, tan);
    expect(Math.hypot(tan[0], tan[1])).toBeCloseTo(1, 5);
    expect(tan[0]).toBeGreaterThan(0);
  });
});

describe("cssColorToHex", () => {
  /* A stand-in for a 2D context: it accepts "#rrggbb" and "rgb(r,g,b)", and
   * rejects everything else the way a real context does, by leaving
   * fillStyle unchanged. */
  function fakeContext() {
    let style = "#000000";
    let painted = [0, 0, 0];
    return {
      get fillStyle() {
        return style;
      },
      set fillStyle(v: string) {
        if (/^#[0-9a-f]{6}$/i.test(v) || /^rgb\(/.test(v)) style = v;
      },
      fillRect() {
        const m = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(style);
        painted = m ? [parseInt(m[1], 16), parseInt(m[2], 16), parseInt(m[3], 16)] : [1, 2, 3];
      },
      getImageData() {
        return { data: painted } as unknown as ImageData;
      },
    };
  }

  it("converts a colour the context understands", () => {
    expect(cssColorToHex("#45b5d0", fakeContext())).toBe(0x45b5d0);
  });

  it("returns null for a value the context rejects, so the fallback is used", () => {
    expect(cssColorToHex("not a colour", fakeContext())).toBeNull();
    expect(cssColorToHex("", fakeContext())).toBeNull();
  });
});
