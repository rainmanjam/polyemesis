import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { OG_CARDS, ogSlugFor } from "../data/og.mjs";

/* Every new route is registered in the six places that each have a check
 * elsewhere; this fails here, with the route's name, before the build does. */
export const NEW_ROUTES = ["/for/sports-clubs", "/for/churches-and-events", "/for/streamers"];

const read = (rel: string) => readFileSync(fileURLToPath(new URL(`../../${rel}`, import.meta.url)), "utf8");
const exists = (rel: string) => existsSync(fileURLToPath(new URL(`../../${rel}`, import.meta.url)));

describe.each(NEW_ROUTES)("%s", (route) => {
  it("has a sitemap source", () => expect(read("astro.config.mjs")).toMatch(new RegExp(`"${route}":`)));
  it("has a cache rule", () => {
    const lines = read("public/_headers").split(/\r?\n/);
    const prefix = route.replace(/\/[^/]*$/, "/*");
    expect(lines.includes(route) || lines.includes(prefix)).toBe(true);
  });
  it("is in llms.txt", () => expect(read("public/llms.txt")).toContain(`https://polyemesis.com${route})`));
  it("is in the layout check", () => expect(read("scripts/check-layout.mjs")).toContain(`"${route}": 0`));
  it("has a link-preview card and image", () => {
    expect(OG_CARDS.some((c: { path: string }) => c.path === route)).toBe(true);
    expect(exists(`public/og/${ogSlugFor(route)}.png`)).toBe(true);
  });
});
