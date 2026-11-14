import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { AUDIENCES } from "../data/audiences.mjs";
import { BY_SLUG } from "../data/docs.mjs";
import { RECIPES, RECIPE_ROUTES } from "../data/recipes.mjs";
import { factProblems, type Fact } from "./facts";

const readDoc = (slug: string): string | undefined => {
  const doc = BY_SLUG.get(slug);
  if (!doc) return undefined;
  return readFileSync(fileURLToPath(new URL(`../../../docs/${doc.file}`, import.meta.url)), "utf8");
};
const shotsDir = fileURLToPath(new URL("../assets/shots/", import.meta.url));
const SLUGS = ["two-languages-one-broadcast", "obs-to-five-platforms", "streaming-from-a-backpack"];

type R = (typeof RECIPES)[number];
const factsOf = (r: R): Fact[] => [...r.needs, ...r.steps.flatMap((s: { facts: Fact[] }) => s.facts), ...r.check];
const stripEnd = (s: string) => s.replace(/<[^>]*>/g, "").replace(/\.\s*$/, "").trim();

describe("recipe data", () => {
  it("has the three recipes, in the homepage's order, at /recipes/<slug>", () => {
    expect(RECIPES.map((r: R) => r.slug)).toEqual(SLUGS);
    for (const r of RECIPES) expect(r.route).toBe(`/recipes/${r.slug}`);
    expect(RECIPE_ROUTES).toEqual(Object.fromEntries(SLUGS.map((s) => [s, `/recipes/${s}`])));
  });

  it("has exactly four steps each, and facts in every step", () => {
    for (const r of RECIPES) {
      expect(r.steps, r.slug).toHaveLength(4);
      for (const s of r.steps) expect(s.facts.length).toBeGreaterThanOrEqual(1);
      expect(r.needs.length).toBeGreaterThanOrEqual(1);
      expect(r.check.length).toBeGreaterThanOrEqual(1);
    }
  });

  it("ties every fact to its doc", () => {
    for (const r of RECIPES) expect(factProblems(factsOf(r), readDoc), r.slug).toEqual([]);
  });

  it("uses the homepage's step titles, in order", () => {
    const use = readFileSync(fileURLToPath(new URL("../components/home/Use.astro", import.meta.url)), "utf8");
    for (const r of RECIPES) {
      let at = use.indexOf(`"${stripEnd(r.heading)}"`);
      expect(at, `${r.slug} on the homepage`).toBeGreaterThan(-1);
      for (const s of r.steps) {
        const next = use.indexOf(`"${stripEnd(s.title)}.`, at);
        expect(next, `${r.slug}: ${s.title}`).toBeGreaterThan(-1);
        at = next;
      }
    }
  });

  it("uses shots that exist", () => {
    for (const r of RECIPES) {
      for (const s of r.steps as { shot?: { file: string; alt: string } }[]) {
        if (!s.shot) continue;
        expect(existsSync(shotsDir + s.shot.file), s.shot.file).toBe(true);
        expect(s.shot.alt.length).toBeGreaterThan(10);
      }
    }
  });

  it("links only published docs", () => {
    for (const r of RECIPES) {
      expect(r.docs.length).toBeGreaterThanOrEqual(1);
      for (const slug of r.docs) expect(BY_SLUG.has(slug), slug).toBe(true);
    }
  });

  it("is what every audience page's related list points at", () => {
    for (const a of Object.values(AUDIENCES) as { related: string[] }[]) {
      for (const slug of a.related) expect(SLUGS).toContain(slug);
    }
  });

  it("keeps titles within 60 characters, descriptions within 160, all unique", () => {
    const all = RECIPES.flatMap((r: R) => [r.title, r.description]);
    for (const r of RECIPES) {
      expect(r.title.length).toBeLessThanOrEqual(60);
      expect(r.description.length).toBeLessThanOrEqual(160);
    }
    expect(new Set(all).size).toBe(all.length);
  });

  it("has one h1 heading with an accent that occurs in it", () => {
    for (const r of RECIPES) expect(r.heading.toLowerCase()).toContain(r.accent.toLowerCase());
  });

  it("avoids the banned words", () => {
    const text = JSON.stringify(RECIPES);
    expect(text).not.toMatch(/bypass|dodge|DMCA|pre-release|Stream Deck\+|minute|totalTime|\b\d+\s+stars?\b/i);
  });

  it("says Facebook is still being verified in the platforms recipe", () => {
    const r = RECIPES.find((x: R) => x.slug === "obs-to-five-platforms");
    const text = r!.steps.flatMap((s: { facts: Fact[] }) => s.facts.map((f) => f.text)).join(" ");
    expect(text).toContain("Facebook is still being verified");
  });

  it("does not use the doc's old wording for RTMP", () => {
    const text = JSON.stringify(RECIPES);
    expect(text).not.toMatch(/standard RTMP/i);
    expect(text).toContain("Classic RTMP carries a single track");
  });
});
