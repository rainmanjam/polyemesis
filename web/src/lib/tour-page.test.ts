import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { BY_SLUG } from "../data/docs.mjs";
import { TOUR } from "../data/tour.mjs";
import { TOUR_PAGE, TOUR_ROUTE } from "../data/tour-page.mjs";
import { factProblems, type Fact } from "./facts";

const here = (rel: string) => fileURLToPath(new URL(rel, import.meta.url));
const readDoc = (slug: string): string | undefined => {
  const doc = BY_SLUG.get(slug);
  return doc ? readFileSync(here(`../../../docs/${doc.file}`), "utf8") : undefined;
};
const page = readFileSync(here("../pages/tour.astro"), "utf8");
const notes = TOUR_PAGE.notes as { facts: Fact[]; docs: string[] }[];

describe("TOUR_PAGE", () => {
  it("is the /tour page, filmed on 0.18.0", () => {
    expect(TOUR_ROUTE).toBe("/tour");
    expect(TOUR_PAGE.filmedVersion).toBe("0.18.0");
  });

  it("has notes for every chapter of the film (a new chapter without notes fails)", () => {
    expect(notes).toHaveLength(TOUR.chapters.length);
  });

  it("has two or three facts per chapter", () => {
    for (const n of notes) {
      expect(n.facts.length).toBeGreaterThanOrEqual(2);
      expect(n.facts.length).toBeLessThanOrEqual(3);
    }
  });

  it("ties every fact to the doc it comes from", () => {
    expect(factProblems(notes.flatMap((n) => n.facts), readDoc)).toEqual([]);
  });

  it("links only published docs", () => {
    for (const n of notes) for (const slug of n.docs) expect(BY_SLUG.has(slug), slug).toBe(true);
  });

  it("does not say what the film cannot show", () => {
    const text = JSON.stringify(notes);
    expect(text).not.toMatch(/real (platform|channel)|connected a|goes? live on youtube/i);
  });
});

describe("tour.astro", () => {
  it("is built from the tour component, the markup helper and the release reader", () => {
    for (const name of ["Tour", "videoLd", "filmedRelease"]) {
      expect(page, name).toMatch(new RegExp(`import[^;]*\\b${name}\\b`));
    }
  });

  it("shows the filmed release, the chapter starts and the markup", () => {
    for (const s of ["Filmed on v", "chapter-", "clock(", "application/ld+json"]) expect(page).toContain(s);
  });

  it("has no page-level style and reads no clock", () => {
    expect(page).not.toContain("<style");
    expect(page).not.toContain("new Date(");
  });

  it("has the sections in order: watch, one per chapter, next", () => {
    expect(page).toContain('id="watch"');
    expect(page).toContain("chapter-${i + 1}");
    expect(page).toContain('id="next"');
  });
});
