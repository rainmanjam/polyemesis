import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { AUDIENCES } from "../data/audiences.mjs";
import { RECIPES } from "../data/recipes.mjs";

const here = (rel: string) => fileURLToPath(new URL(rel, import.meta.url));
const read = (rel: string) => readFileSync(here(rel), "utf8");
const layout = read("../layouts/RecipePage.astro");

describe("RecipePage.astro", () => {
  it("is built from the shared parts", () => {
    for (const name of ["InnerPage", "Steps", "FactList", "DocLinks", "howToLd"]) {
      expect(layout, name).toMatch(new RegExp(`import[^;]*\\b${name}\\b`));
    }
    expect(layout).toMatch(/\blightbox\b/);
  });

  it("emits the HowTo from the recipe, as JSON-LD", () => {
    expect(layout).toContain("application/ld+json");
    expect(layout).toContain("howToLd(recipe)");
  });

  it("has its sections in order", () => {
    const ids = [...layout.matchAll(/<section id="([a-z]+)"/g)].map((m) => m[1]);
    expect(ids).toEqual(["needs", "steps", "check", "more"]);
  });

  it("has no page-level style", () => {
    expect(layout).not.toContain("<style");
  });

  it("links the multistream guide on the platforms recipe only", () => {
    expect(layout).toContain("/how-to-multistream-from-obs");
    expect(layout).toMatch(/obs-to-five-platforms/);
  });
});

describe("the three recipe pages", () => {
  for (const r of RECIPES as { slug: string }[]) {
    it(`${r.slug} only hands its recipe to the layout`, () => {
      expect(existsSync(here(`../pages/recipes/${r.slug}.astro`))).toBe(true);
      const src = read(`../pages/recipes/${r.slug}.astro`);
      expect(src).toContain("RECIPES.");
      const body = src.split(/^---$/m)[2] ?? "";
      expect(body.trim()).toMatch(/^<RecipePage recipe=\{[^\n]*\}\s*\/>$/);
      expect(body).toContain(`"${r.slug}"`);
    });
  }
});

describe("the audience pages' Next section", () => {
  // AudiencePage.astro links a recipe only once its page file exists; with the
  // three recipes built, every related recipe must therefore be linked.
  it("has a page file for every recipe an audience relates to, so none is dropped", () => {
    for (const a of Object.values(AUDIENCES) as { key: string; related: string[] }[]) {
      expect(a.related.length, a.key).toBeGreaterThanOrEqual(1);
      for (const slug of a.related) expect(existsSync(here(`../pages/recipes/${slug}.astro`)), `${a.key} -> ${slug}`).toBe(true);
    }
  });

  it("still filters by page file in the layout, and renders what is left", () => {
    const audience = read("../layouts/AudiencePage.astro");
    expect(audience).toContain('import.meta.glob("../pages/recipes/*.astro")');
    expect(audience).toContain("recipes(a.related)");
  });
});
