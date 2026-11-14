/* The routing diagram's model and geometry, from one case. RoutingDiagram.astro
 * only draws what this returns, so the numbers live where they are tested.
 *
 * Source on the left, tracks in the middle, destinations on the right.
 * Off-white is video (one copy to every destination); coral is an audio lane a
 * destination carries. The numbers are the ones the homepage drawing already
 * used: a 600-wide viewBox with 13px text, shown only where its container is
 * at least 640px wide, so the text is never smaller than 12px on screen
 * (13 * 640/600 = 13.9). Below that the same routing is the plain list. */
import { mixLabel } from "../data/cases.mjs";
import type { Case } from "./facts";

export const DIAGRAM_WIDTH = 600; /* viewBox width */
export const DIAGRAM_FONT = 13; /* px in viewBox units */
export const DIAGRAM_MIN_PX = 640; /* container width from which the SVG is shown */

export interface DiagramModel {
  source: string;
  spec: string;
  tracks: { id: string; label: string }[];
  dests: { name: string; city: string; mix: string; carries: string[] }[];
}

/** What the model reads of a case: a Case plus the parts the drawing labels. */
export interface DiagramCase extends Case {
  source: { label: string; city: string; spec: string };
  dests: { name: string; city: string; tracks: string[] }[];
}

export function diagramModel(c: DiagramCase): DiagramModel {
  return {
    source: `${c.source.label} · ${c.source.city}`,
    spec: c.source.spec,
    tracks: c.tracks.map((t) => ({ id: t.id, label: t.label })),
    dests: c.dests.map((d) => ({
      name: d.name,
      city: d.city,
      // mixLabel only reads the case's tracks and the destination's track ids.
      mix: mixLabel(c as Parameters<typeof mixLabel>[0], d as Parameters<typeof mixLabel>[1]),
      carries: d.tracks,
    })),
  };
}

const TOP = 40; /* room for the video bus above the rows */
const PITCH = 36;
const BOX_H = 28;
const SRC = { x: 0, w: 124 };
const TRK = { x: 184, w: 116 };
const BUS_X = 366;
const DST = { x: 388, w: DIAGRAM_WIDTH - 388 };

export function layoutRows(m: DiagramModel): {
  height: number;
  trackY: number[];
  destY: number[];
  links: { track: number; dest: number }[];
} {
  const rows = Math.max(m.tracks.length, m.dests.length);
  const rowY = (i: number, count: number) => TOP + ((rows - count) / 2 + i) * PITCH;
  const links: { track: number; dest: number }[] = [];
  m.dests.forEach((d, dest) => {
    for (const id of d.carries) {
      const track = m.tracks.findIndex((t) => t.id === id);
      if (track >= 0) links.push({ track, dest });
    }
  });
  return {
    height: TOP + rows * PITCH + 4,
    trackY: m.tracks.map((_, i) => rowY(i, m.tracks.length)),
    destY: m.dests.map((_, i) => rowY(i, m.dests.length)),
    links,
  };
}

export interface Box {
  x: number;
  y: number;
  w: number;
  h: number;
}

/** Everything RoutingDiagram.astro draws, as numbers and path strings. */
export function diagramShapes(m: DiagramModel) {
  const { height, trackY, destY, links } = layoutRows(m);
  const rows = Math.max(m.tracks.length, m.dests.length);
  const mid = TOP + (rows * PITCH - (PITCH - BOX_H)) / 2;
  const lastMid = destY[destY.length - 1] + BOX_H / 2;
  const x1 = TRK.x + TRK.w;
  const bend = (DST.x - x1) * 0.55;
  const midY = (y: number) => y + BOX_H / 2;

  const box = (x: number, y: number, w: number): Box => ({ x, y, w, h: BOX_H });
  return {
    height,
    source: { x: SRC.x, y: mid - BOX_H, w: SRC.w, h: BOX_H * 2 } as Box,
    tracks: trackY.map((y) => box(TRK.x, y, TRK.w)),
    dests: destY.map((y) => box(DST.x, y, DST.w)),
    videoLabel: { x: SRC.x + SRC.w / 2 + 10, y: TOP - 20 },
    videoPaths: [
      `M${SRC.x + SRC.w / 2},${mid - BOX_H} V${TOP - 14} H${BUS_X} V${lastMid}`,
      ...destY.map((y) => `M${BUS_X},${midY(y)} H${DST.x}`),
    ],
    sourcePaths: trackY.map(
      (y) => `M${SRC.x + SRC.w},${mid} C${TRK.x - 30},${mid} ${TRK.x - 30},${midY(y)} ${TRK.x},${midY(y)}`,
    ),
    audioPaths: links.map((l) => {
      const ya = midY(trackY[l.track]);
      const yb = midY(destY[l.dest]);
      return `M${x1},${ya} C${x1 + bend},${ya} ${DST.x - bend},${yb} ${DST.x},${yb}`;
    }),
    /** Text anchors: a box's left edge plus 10, baselines as the old drawing set them. */
    sourceName: { x: SRC.x + 10, y: mid - 2 },
    sourceCity: { x: SRC.x + 10, y: mid + 16 },
    trackText: trackY.map((y) => ({ x: TRK.x + 10, y: y + 19 })),
    destText: destY.map((y) => ({ x: DST.x + 10, y: y + 19 })),
  };
}
