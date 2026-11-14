import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { CASE_ORDER, CASES } from "../data/cases.mjs";
import { DIAGRAM_FONT, DIAGRAM_MIN_PX, DIAGRAM_WIDTH, diagramModel, diagramShapes, layoutRows } from "./routing-diagram";

const here = dirname(fileURLToPath(import.meta.url));
const read = (...p: string[]) => readFileSync(join(here, "..", ...p), "utf8");

describe("diagram model and layout", () => {
  const model = diagramModel(CASES.sports);

  it("reads the sports case", () => {
    expect(model.source).toBe("match feed · Manchester");
    expect(model.spec).toBe(CASES.sports.source.spec);
    expect(model.tracks).toHaveLength(6);
    expect(model.dests).toHaveLength(5);
    expect(model.dests[0].mix).toBe("video + ENGLISH + CROWD");
    expect(model.dests[0].carries).toEqual(["english", "crowd"]);
  });

  it("makes one link per carried track", () => {
    const rows = layoutRows(model);
    expect(rows.links).toHaveLength(10);
    for (const l of rows.links) {
      expect(l.track).toBeGreaterThanOrEqual(0);
      expect(l.track).toBeLessThan(model.tracks.length);
      expect(l.dest).toBeGreaterThanOrEqual(0);
      expect(l.dest).toBeLessThan(model.dests.length);
    }
    expect(rows.trackY).toHaveLength(6);
    expect(rows.destY).toHaveLength(5);
    expect(rows.height).toBeGreaterThan(0);
  });

  it("joins only tracks a destination carries, in every case", () => {
    for (const key of CASE_ORDER) {
      const m = diagramModel(CASES[key]);
      const { links } = layoutRows(m);
      for (const l of links) expect(m.dests[l.dest].carries).toContain(m.tracks[l.track].id);
      expect(links).toHaveLength(m.dests.reduce((n, d) => n + d.carries.length, 0));
    }
  });

  it("centres the shorter column against the taller one", () => {
    const rows = layoutRows(model);
    expect(rows.trackY[0]).toBeLessThan(rows.destY[0]);
  });

  it("drops a carried id that is not a track instead of drawing a stray line", () => {
    const m = { ...model, dests: [{ ...model.dests[0], carries: ["english", "ghost"] }] };
    expect(layoutRows(m).links).toHaveLength(1);
  });
});

describe("drawing numbers", () => {
  it("shows the diagram only from 640px, with text no smaller than 12px on screen", () => {
    expect(DIAGRAM_MIN_PX).toBe(640);
    expect(DIAGRAM_FONT * (DIAGRAM_MIN_PX / DIAGRAM_WIDTH)).toBeGreaterThanOrEqual(12);
  });

  it("keeps the numbers Audiences.astro drew with (600 wide, 13px)", () => {
    expect(DIAGRAM_WIDTH).toBe(600);
    expect(DIAGRAM_FONT).toBe(13);
  });

  it("stays inside the viewBox width", () => {
    for (const key of CASE_ORDER) {
      const s = diagramShapes(diagramModel(CASES[key]));
      for (const b of [s.source, ...s.tracks, ...s.dests]) expect(b.x + b.w).toBeLessThanOrEqual(DIAGRAM_WIDTH);
    }
  });
});

describe("drawn shapes", () => {
  it("has a video line to every destination and one audio path per link", () => {
    const m = diagramModel(CASES.worship);
    const s = diagramShapes(m);
    expect(s.videoPaths).toHaveLength(1 + m.dests.length);
    expect(s.audioPaths).toHaveLength(layoutRows(m).links.length);
    expect(s.sourcePaths).toHaveLength(m.tracks.length);
  });
});

describe("RoutingDiagram.astro, new-pages.css and Audiences.astro", () => {
  const comp = read("components", "RoutingDiagram.astro");
  const css = read("styles", "new-pages.css");
  const aud = read("components", "home", "Audiences.astro");
  const global = read("styles", "global.css");

  it("holds no name literal, no style block, hides the svg from assistive technology and keeps the list", () => {
    for (const key of CASE_ORDER) {
      for (const n of [...CASES[key].tracks.map((t: { label: string }) => t.label), ...CASES[key].dests.map((d: { name: string }) => d.name)]) {
        if (n.length >= 3) expect(comp, `literal "${n}"`).not.toContain(n);
      }
    }
    expect(comp).not.toContain("<style");
    expect(comp).toContain('aria-hidden="true"');
    expect(comp).toContain("diag-list");
    expect(comp).not.toMatch(/<title|\bid=/);
  });

  it("moves the container rule into new-pages.css, with no entrance motion and no faint text colour", () => {
    expect(css).toMatch(/container-type:\s*inline-size/);
    expect(css).toContain("@container (min-width: 640px)");
    expect(css).not.toMatch(/\.reveal|data-reveal/);
    expect(css).not.toContain("--color-faint");
  });

  it("is imported by global.css after inner.css", () => {
    expect(global.indexOf('@import "./new-pages.css";')).toBeGreaterThan(global.indexOf('@import "./inner.css";'));
  });

  it("is used by the homepage block, which no longer draws its own svg", () => {
    expect(aud).toContain("RoutingDiagram");
    expect(aud).not.toContain("<svg");
  });
});
