import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { CASE_ORDER, CASES } from "../data/cases.mjs";

/* Source-text guards for the audiences section. The routing diagrams and the
 * recipes read their names from cases.mjs, so the component must hold none of
 * them as a literal, or a rename there would leave this section behind. */

const here = dirname(fileURLToPath(import.meta.url));
const read = (...p: string[]) => readFileSync(join(here, "..", ...p), "utf8");
const src = read("components", "home", "Audiences.astro");
const markup = src.slice(src.indexOf("---", 3) + 3);

const names = new Set<string>();
for (const key of CASE_ORDER) {
  for (const t of CASES[key].tracks) names.add(t.label);
  for (const d of CASES[key].dests) names.add(d.name);
}

describe("audiences section", () => {
  it("is the understand-stage section with its id", () => {
    expect(src).toMatch(/<section[^>]*id="audiences"/);
    expect(src).toMatch(/data-journey-stage="understand"/);
  });

  it("maps over CASE_ORDER and renders each case's fine print", () => {
    expect(src).toMatch(/CASE_ORDER\.map/);
    expect(src).toMatch(/\{c\.fine\}|\{CASES\[key\]\.fine\}/);
  });

  it("holds no destination or track name as a literal", () => {
    for (const n of names) {
      if (n.length < 3) continue;
      const found = src.split("\n").filter((l) => !l.trim().startsWith("*") && l.includes(n));
      expect(found, `literal "${n}"`).toEqual([]);
    }
  });

  it("draws the diagram only where its container is 640px or wider, with no script", () => {
    expect(src).toMatch(/container-type:\s*inline-size/);
    expect(src).toMatch(/@container[^{]*\(min-width:\s*640px\)/);
    expect(src).not.toMatch(/<script/);
  });

  it("keeps every font size at 12px or more, in the viewBox scale too", () => {
    const sizes = [...src.matchAll(/font-size:\s*([\d.]+)(px|rem)/g)].map((m) => (m[2] === "rem" ? +m[1] * 16 : +m[1]));
    expect(sizes.length).toBeGreaterThan(0);
    for (const s of sizes) expect(s).toBeGreaterThanOrEqual(12);
    const vb = src.match(/const VIEW_W = (\d+)/);
    expect(vb && +vb[1]).toBeLessThanOrEqual(600);
  });

  it("uses no entrance animation and none of the banned copy", () => {
    expect(src).not.toMatch(/reveal|data-reveal/);
    expect(markup).not.toMatch(/three steps|minutes|bypass|dodge|DMCA|pre-release|one maintainer/i);
  });

  it("links each recipe to a doc that exists", () => {
    const hrefs = [...src.matchAll(/\/docs\/([a-z-]+)/g)].map((m) => m[1]);
    expect(hrefs.length).toBe(CASE_ORDER.length);
    for (const h of hrefs) expect(["audio-routing", "obs", "quickstart", "renditions", "srtla", "connecting-platforms", "platforms"]).toContain(h);
  });

  it("sits right after the mix matrix on the home page", () => {
    const page = read("pages", "index.astro");
    expect(page).toMatch(/<MixMatrixPlay \/>\s*<Audiences \/>/);
  });

  /* The layout and the diagram guard must move together: the figure gets 7fr
   * of the row less a 56px gap, and the diagram needs 640px of it. */
  it("only goes side by side when the figure still has 640px", () => {
    const bp = src.match(/@media \(min-width: (\d+)px\) \{\s*\.aud \{\s*grid-template-columns: minmax\(0, 7fr\) minmax\(0, 5fr\);\s*gap: 56px/);
    expect(bp, "side-by-side breakpoint").not.toBeNull();
    const query = +(src.match(/@container aud \(min-width: (\d+)px\)/)?.[1] ?? 0);
    expect(query).toBe(640);
    /* the .wrap is 1320px max with at least 40px gutters each side */
    const row = Math.min(+bp![1], 1320) - 80;
    expect(((row - 56) * 7) / 12).toBeGreaterThanOrEqual(query);
  });
});
