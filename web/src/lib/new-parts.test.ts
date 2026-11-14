import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { CASE_ORDER, CASES } from "../data/cases.mjs";

/* Source-text guards for the shared parts of the new pages. */

const here = dirname(fileURLToPath(import.meta.url));
const read = (...p: string[]) => readFileSync(join(here, "..", ...p), "utf8");
const steps = read("components", "inner", "Steps.astro");
const factList = read("components", "inner", "FactList.astro");
const docLinks = read("components", "inner", "DocLinks.astro");
const css = read("styles", "new-pages.css");

const names = new Set<string>();
for (const key of CASE_ORDER) {
  for (const t of CASES[key].tracks) names.add(t.label);
  for (const d of CASES[key].dests) names.add(d.name);
}

describe("Steps.astro", () => {
  it("renders a real numbered sequence with one id per step", () => {
    expect(steps).toContain('<ol class="steps"');
    expect(steps).toContain("id={`${idPrefix}-${");
  });
  it("shows each step's shot with Shot, and links its docs from the facts", () => {
    expect(steps).toMatch(/import Shot from "\.\/Shot\.astro"/);
    expect(steps).toContain("docHref");
    expect(steps).toContain("BY_SLUG");
    expect(steps).toContain("BY_SLUG.get(");
    expect(steps).toContain("p class=\"fact\"");
    expect(steps).toContain("docrow");
  });
});

describe("FactList.astro", () => {
  it("ends with the install row", () => {
    expect(factList).toContain('href="/download"');
    expect(factList).toContain("Install polyemesis");
    expect(factList).toContain("polyemesis installed and running");
  });
});

describe("DocLinks.astro", () => {
  it("looks each slug up and throws at build for an unknown one", () => {
    expect(docLinks).toContain("BY_SLUG.get(");
    expect(docLinks).toContain("throw");
    expect(docLinks).toContain("hrefOf");
  });
});

describe("all three and their styles", () => {
  it("hold no style block, no entrance motion and no destination or track name", () => {
    for (const src of [steps, factList, docLinks]) {
      expect(src).not.toContain("<style");
      expect(src).not.toMatch(/\.reveal|data-reveal/);
      for (const n of names) if (n.length >= 3) expect(src, `literal "${n}"`).not.toContain(n);
    }
  });
  it("define the classes the parts use, at 12px or more, never in the faint colour", () => {
    for (const c of [".steps", ".step", ".fact", ".docrow", ".fine", ".chapter"]) expect(css).toContain(c);
    expect(css).not.toContain("--color-faint");
    const sizes = [...css.matchAll(/font-size:\s*([\d.]+)(px|rem)/g)].map((m) => (m[2] === "rem" ? +m[1] * 16 : +m[1]));
    for (const s of sizes) expect(s).toBeGreaterThanOrEqual(12);
  });
  it("cap .fine at 60ch and give the step number a mono 12px label", () => {
    expect(css).toMatch(/\.fine[^{]*\{[^}]*max-width:\s*60ch/);
    expect(css).toMatch(/\.step::before[^{]*\{[^}]*font-size:\s*0\.75rem/);
  });
});
