import { existsSync, readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { AUDIENCE_ROUTES } from "../data/audiences.mjs";
import { RECIPES } from "../data/recipes.mjs";
import { TOUR_ROUTE } from "../data/tour-page.mjs";

/* The homepage and footer reach the seven new pages only through data, and a
 * route in the data cannot exist without a page file. */
const abs = (rel: string) => fileURLToPath(new URL(`../${rel}`, import.meta.url));
const src = (rel: string) => readFileSync(abs(rel), "utf8");

const routes = [...Object.values(AUDIENCE_ROUTES), ...RECIPES.map((r) => r.route), TOUR_ROUTE];
const hasPage = (route: string) => existsSync(abs(`pages${route}.astro`));

describe("links to the new pages", () => {
  it("every route in the data has a page file", () => {
    expect(routes).toHaveLength(7);
    for (const route of routes) expect(hasPage(route), route).toBe(true);
  });

  it("the homepage sections read the routes from data", () => {
    expect(src("components/home/Audiences.astro")).toContain("AUDIENCE_ROUTES");
    const use = src("components/home/Use.astro");
    expect(use).toContain('import { RECIPES } from "../../data/recipes.mjs"');
    expect(use).toContain(".route");
    expect(src("components/home/HowItWorks.astro")).toContain("TOUR_ROUTE");
    expect(src("components/home/Audiences.astro")).toContain('from "../../data/audiences.mjs"');
  });

  it("no homepage file types a route to a new page", () => {
    const files = readdirSync(abs("components/home"))
      .filter((f) => f.endsWith(".astro"))
      .map((f) => `components/home/${f}`)
      .concat("pages/index.astro");
    for (const f of files) {
      const text = src(f);
      for (const typed of ["/for/", "/recipes/", '"/tour"']) expect(text, `${f} types ${typed}`).not.toContain(typed);
    }
  });

  it("the footer links the tour and the three audience pages, not the recipes", () => {
    const footer = src("components/Footer.astro");
    for (const h of ["/tour", "/for/sports-clubs", "/for/churches-and-events", "/for/streamers"]) {
      expect(footer).toContain(`href: "${h}"`);
    }
    expect(footer).not.toContain("/recipes/");
    const hrefs = [...footer.matchAll(/href: "(\/(?:for\/[^"]*|tour))"/g)].map((m) => m[1]);
    for (const h of hrefs) expect(hasPage(h), h).toBe(true);
  });

  it("each recipe block on the homepage has its four step titles once", () => {
    const use = src("components/home/Use.astro");
    for (const r of RECIPES) {
      expect(r.steps).toHaveLength(4);
      for (const s of r.steps) {
        const n = use.split(`"${s.title}."`).length - 1;
        expect(n, s.title).toBe(1);
      }
    }
  });
});
