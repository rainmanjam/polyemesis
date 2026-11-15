import { describe, expect, it } from "vitest";

import { AUDIENCES } from "../data/audiences.mjs";
import { RECIPES } from "../data/recipes.mjs";
import { TOUR_PAGE } from "../data/tour-page.mjs";

interface Meta {
  title: string;
  description: string;
  heading: string;
  accent: string;
}

/* The seven pages of plan 4, and the rules their titles, descriptions and
 * headings share. */
const pages: Meta[] = [...Object.values(AUDIENCES as Record<string, Meta>), ...(RECIPES as Meta[]), TOUR_PAGE as Meta];
const BANNED = /bypass|dodge|DMCA|pre-release|Stream Deck\+|minute|\b\d+\s+stars?\b/i;

describe("the seven new pages", () => {
  it("are seven", () => expect(pages).toHaveLength(7));

  it("have titles of at most 60 characters and descriptions of at most 160", () => {
    for (const p of pages) {
      expect(p.title.length, p.title).toBeLessThanOrEqual(60);
      expect(p.description.length, p.description).toBeLessThanOrEqual(160);
    }
  });

  it("are all different from each other, in all 14 strings", () => {
    const strings = pages.flatMap((p) => [p.title, p.description]);
    expect(strings).toHaveLength(14);
    expect(new Set(strings).size).toBe(14);
  });

  it("use none of the words the site does not use", () => {
    for (const p of pages) {
      for (const s of [p.title, p.description, p.heading]) expect(s).not.toMatch(BANNED);
    }
  });

  it("have a heading that contains its accent exactly once", () => {
    for (const p of pages) expect(p.heading.split(p.accent).length - 1, p.heading).toBe(1);
  });
});
