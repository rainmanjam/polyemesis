import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { AUDIENCES } from "../data/audiences.mjs";

const read = (rel: string) => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), "utf8");
const layout = read("../layouts/AudiencePage.astro");

describe("AudiencePage.astro", () => {
  it("is built from the shared parts", () => {
    for (const name of ["InnerPage", "RoutingDiagram", "Steps", "FactList", "DocLinks", "fillCase"]) {
      expect(layout, name).toMatch(new RegExp(`import[^;]*\\b${name}\\b`));
    }
    expect(layout).toMatch(/\blightbox\b/);
  });

  it("has its sections in order", () => {
    const ids = [...layout.matchAll(/<section id="([a-z]+)"/g)].map((m) => m[1]);
    expect(ids).toEqual(["diagram", "need", "setup", "features", "next"]);
  });

  it("carries the fine print and the matrix link, and no page-level style", () => {
    expect(layout).toContain("data-fine");
    expect(layout).toContain('class="fine"');
    expect(layout).toContain('href="/#matrix"');
    expect(layout).not.toContain("<style");
  });

  it("renders the fine print once, straight after the diagram", () => {
    expect(layout.match(/data-fine/g)).toHaveLength(1);
    expect(layout).toMatch(/<RoutingDiagram[^>]*\/>\s*<p class="fine" data-fine>\{c\.fine\}<\/p>/);
  });

  it("shows the demo caption on every non-sports shot and the sports page's none", () => {
    for (const a of Object.values(AUDIENCES)) {
      for (const s of a.steps as { shot?: { caption?: string } }[]) {
        if (!s.shot) continue;
        if (a.key === "sports") expect(s.shot.caption).toBeUndefined();
        else expect(s.shot.caption).toMatch(/^Demo install: /);
      }
    }
  });
});

describe("the three page files", () => {
  for (const [file, key] of [["sports-clubs", "sports"], ["churches-and-events", "worship"], ["streamers", "streamer"]]) {
    it(`${file} only hands its audience to the layout`, () => {
      const src = read(`../pages/for/${file}.astro`);
      expect(src).toContain(`AUDIENCES.${key}`);
      const body = src.split(/^---$/m)[2] ?? "";
      expect(body.replace(/<[^>]+>/g, "").trim()).toBe("");
    });
  }
});
