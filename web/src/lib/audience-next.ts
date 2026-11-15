/* What the audience pages build from data: the recipes their "Next" section
 * links (title and route from the recipe data) and the lines that say what
 * each destination hears. AudiencePage.astro only calls these. */
import { RECIPES } from "../data/recipes.mjs";
import { mixLabel } from "../data/cases.mjs";
import type { Case } from "./facts";

type RecipeRow = { slug: string; route: string; heading: string };
type Dest = Parameters<typeof mixLabel>[1];

/** One row per related slug, in order. An unknown slug throws, so a missing link fails the build. */
export function relatedRecipes(slugs: string[]): { title: string; href: string }[] {
  return slugs.map((slug) => {
    const r = (RECIPES as RecipeRow[]).find((x) => x.slug === slug);
    if (!r) throw new Error(`audience-next: "${slug}" is not a recipe (data/recipes.mjs).`);
    return { title: r.heading.replace(/\.$/, ""), href: r.route };
  });
}

/** "<destination>: <what it hears>", one per destination of the case. */
export function destinationNotes(c: Case & { dests: Dest[] }): string[] {
  return c.dests.map((d) => `${d.name}: ${mixLabel(c as Parameters<typeof mixLabel>[0], d)}`);
}
