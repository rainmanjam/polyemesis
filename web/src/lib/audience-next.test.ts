import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { AUDIENCES } from "../data/audiences.mjs";
import { CASES, mixLabel } from "../data/cases.mjs";
import { RECIPES } from "../data/recipes.mjs";
import { destinationNotes, relatedRecipes } from "./audience-next";

const here = (rel: string) => fileURLToPath(new URL(rel, import.meta.url));
const audiences = Object.values(AUDIENCES) as { key: "sports" | "worship" | "streamer"; related: string[] }[];

describe("relatedRecipes", () => {
  it("resolves every related slug of every audience to a recipe with a page file", () => {
    for (const a of audiences) {
      for (const slug of a.related) {
        expect(RECIPES.some((r: { slug: string }) => r.slug === slug), `${a.key} -> ${slug}`).toBe(true);
        expect(existsSync(here(`../pages/recipes/${slug}.astro`)), `${a.key} -> ${slug}`).toBe(true);
      }
    }
  });

  it("gives, for each audience, one row per related slug in order, with the recipe's title and route", () => {
    for (const a of audiences) {
      const rows = relatedRecipes(a.related);
      expect(rows.map((r) => r.href)).toEqual(a.related.map((s) => `/recipes/${s}`));
      for (const row of rows) {
        const recipe = RECIPES.find((r: { route: string }) => r.route === row.href);
        expect(row.title).toBe(recipe!.heading.replace(/\.$/, ""));
      }
    }
  });

  it("throws on a slug that is not a recipe, so a missing link fails loudly", () => {
    expect(() => relatedRecipes(["no-such-recipe"])).toThrow(/no-such-recipe/);
  });
});

describe("destinationNotes", () => {
  it("has one entry per destination: its name and what it hears", () => {
    for (const a of audiences) {
      const c = CASES[a.key];
      const notes = destinationNotes(c);
      expect(notes).toHaveLength(c.dests.length);
      c.dests.forEach((d: { name: string }, i: number) => expect(notes[i]).toBe(`${d.name}: ${mixLabel(c, d)}`));
    }
  });
});

describe("AudiencePage.astro", () => {
  const src = readFileSync(here("../layouts/AudiencePage.astro"), "utf8");
  it("only calls the library functions: no file glob, no copied titles", () => {
    expect(src).toContain("relatedRecipes(");
    expect(src).toContain("destinationNotes(");
    expect(src).not.toContain("import.meta.glob");
    expect(src).not.toMatch(/Two languages, one broadcast|OBS to five platforms|Streaming from a backpack/);
  });
});
