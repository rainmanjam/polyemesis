/* Geometry for the split diagram (SplitDiagram.astro).
 *
 * The diagram is cards on a CSS grid with an SVG of lines behind them. The SVG
 * is stretched to the grid box (preserveAspectRatio="none", non-scaling
 * strokes), so a point here is a FRACTION of the box, not a pixel. Every anchor
 * is the centre of a grid cell, and SplitDiagram's grid uses the same fractions,
 * so a line always ends under the card it belongs to at any width.
 *
 *   h (>= 768px)  1000 x 360. Three columns: sources centred at 9%, the hub at
 *                 50%, destinations at 91%. Source rows are (i + .5) / 5,
 *                 destination rows (i + .5) / n, the hub sits mid-height.
 *   v (< 768px)   360 x 560. Sources are a top row of five, the hub sits in
 *                 the middle, destinations are a 2 x 2 grid at the bottom.
 *                 Row bands, as fractions of the height: sources 0-.20,
 *                 hub .32-.48, destinations .56-1 (two rows of .22).
 *
 * If you change a fraction here, change the matching grid track in
 * SplitDiagram.astro; check-home I measures that they still meet.
 *
 * Pure: no DOM. Coordinates are rounded to one decimal so the server-rendered
 * paths are short and stable between builds. */

import type { Story } from "./stories";

export type Layout = "h" | "v";

export const VIEWBOX: Record<Layout, { w: number; h: number }> = {
  h: { w: 1000, h: 360 },
  v: { w: 360, h: 560 },
};

export interface Pt { x: number; y: number }

export interface Line {
  d: string;
  kind: "video" | "audio";
  /** Source index (0 is the video, 1-4 the tracks) or the hub. */
  from: number | "hub";
  /** "hub" or a destination id. */
  to: string | "hub";
  /** The how-it-works steps this line is visible on. */
  on: string;
}

/** Step visibility, shared with SplitDiagram's chips and cards. */
export const ON = {
  srcVideo: "1 2 5",
  srcTrack: "1 3 4 5",
  hub: "2 3 4 5",
  dstVideo: "2 5",
  dstAudio: "4 5",
  dest: "2 4 5",
} as const;

const SOURCES = 5; // the video plus four tracks

/* v-layout row bands (fractions of the height), mirrored by the grid. */
const V_SRC_Y = 0.1;
const V_HUB_Y = 0.4;
const V_DST_TOP = 0.56;

const r1 = (n: number): number => Math.round(n * 10) / 10;
const pt = (x: number, y: number): Pt => ({ x: r1(x), y: r1(y) });
const centre = (i: number, n: number): number => (i + 0.5) / n;

export function anchors(layout: Layout, dests: number): { src: Pt[]; hub: Pt; dst: Pt[] } {
  const { w, h } = VIEWBOX[layout];
  const range = (n: number) => Array.from({ length: n }, (_, i) => i);
  if (layout === "h") {
    return {
      src: range(SOURCES).map((i) => pt(0.09 * w, centre(i, SOURCES) * h)),
      hub: pt(0.5 * w, 0.5 * h),
      dst: range(dests).map((i) => pt(0.91 * w, centre(i, dests) * h)),
    };
  }
  const cols = 2;
  const rows = Math.ceil(dests / cols);
  const band = 1 - V_DST_TOP;
  return {
    src: range(SOURCES).map((i) => pt(centre(i, SOURCES) * w, V_SRC_Y * h)),
    hub: pt(0.5 * w, V_HUB_Y * h),
    dst: range(dests).map((i) =>
      pt(centre(i % cols, cols) * w, (V_DST_TOP + centre(Math.floor(i / cols), rows) * band) * h),
    ),
  };
}

/* One cubic Bezier from a to b, leaving and arriving along the layout's flow
 * axis. `pull` sets how far the two control points reach along that axis; the
 * video and audio lines to the same card use different pulls so they read as
 * two lines rather than one drawn twice. */
function curve(layout: Layout, a: Pt, b: Pt, pull: [number, number]): string {
  const [p, q] = pull;
  const c1 = layout === "h" ? pt(a.x + (b.x - a.x) * p, a.y) : pt(a.x, a.y + (b.y - a.y) * p);
  const c2 = layout === "h" ? pt(a.x + (b.x - a.x) * q, b.y) : pt(b.x, a.y + (b.y - a.y) * q);
  return `M${a.x} ${a.y} C${c1.x} ${c1.y}, ${c2.x} ${c2.y}, ${b.x} ${b.y}`;
}

/* A card in the second row of the v layout sits under a card of the first row,
 * and a curve straight to its centre would pass behind that card (the cards sit
 * above the lines). Its lines go down the seam between the two columns and hook
 * into the card from the side, under it: one cubic whose control points stay on
 * the seam. `off` nudges the audio line a little to the card's side of the seam
 * so it does not lie on the video line. */
const SEAM_OFF = 2;
function seam(a: Pt, b: Pt, off: number): string {
  const x = a.x + off;
  return `M${a.x} ${a.y} C${r1(x)} ${b.y}, ${r1(x)} ${b.y}, ${b.x} ${b.y}`;
}

const SRC_PULL: [number, number] = [0.5, 0.5];
const VIDEO_PULL: [number, number] = [0.3, 0.7];
const AUDIO_PULL: [number, number] = [0.65, 0.35];

/** Every line the diagram draws: five source lines into the hub, then for each
 * destination a video line (when it takes video) and one audio line (its mix). */
export function linesFor(story: Story, layout: Layout): Line[] {
  const a = anchors(layout, story.dests.length);
  const lines: Line[] = a.src.map((p, i) => ({
    d: curve(layout, p, a.hub, SRC_PULL),
    kind: i === 0 ? "video" : "audio",
    from: i,
    to: "hub",
    on: i === 0 ? ON.srcVideo : ON.srcTrack,
  }));
  story.dests.forEach((dest, j) => {
    const to = a.dst[j];
    const lower = layout === "v" && to.y > a.dst[0].y;
    const side = to.x < a.hub.x ? 1 : -1;
    if (dest.video) {
      lines.push({ d: lower ? seam(a.hub, to, 0) : curve(layout, a.hub, to, VIDEO_PULL), kind: "video", from: "hub", to: dest.id, on: ON.dstVideo });
    }
    lines.push({
      d: lower ? seam(a.hub, to, side * SEAM_OFF) : curve(layout, a.hub, to, AUDIO_PULL),
      kind: "audio",
      from: "hub",
      to: dest.id,
      on: ON.dstAudio,
    });
  });
  return lines;
}
