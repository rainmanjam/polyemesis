import { readFileSync, readdirSync, existsSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { CASES } from "../data/cases.mjs";
import { DOCKER_RUN } from "../data/install.mjs";
import { PUBLISHED } from "../data/docs.mjs";
import { OG_CARDS, OG_SLUGS, formatOgManifest, hasOtherCanonical, ogHash, ogSlugFor, parseOgManifest } from "../data/og.mjs";
import { pageSource } from "../../astro.config.mjs";

/* Per-page link-preview images. The route list comes from `pageSource` in
 * astro.config.mjs and the published docs from PUBLISHED, never from a copy
 * here, so a route added later without a card fails this file. */

const web = (p: string) => fileURLToPath(new URL(`../../${p}`, import.meta.url));
const MAX_BYTES = 150_000;

const template = () => readFileSync(web("og/card.html"), "utf8");

describe("ogSlugFor", () => {
  it("maps a path to a file-safe slug", () => {
    expect(ogSlugFor("/")).toBe("home");
    expect(ogSlugFor("/docs/audio-routing")).toBe("docs-audio-routing");
    expect(ogSlugFor("/vs/restreamer")).toBe("vs-restreamer");
  });
});

describe("OG_CARDS", () => {
  const slugs = OG_CARDS.map((c: { slug: string }) => c.slug);

  it("has a unique slug per card", () => {
    expect(new Set(slugs).size).toBe(slugs.length);
  });

  it("has exactly one card for every route in pageSource and every published doc", () => {
    const routes = new Set<string>([
      ...Object.keys(pageSource).filter((r) => !PUBLISHED.some((d: { slug: string; canonical?: string }) => r === `/docs/${d.slug}` && hasOtherCanonical(d))),
    ]);
    for (const route of routes) {
      const slug = ogSlugFor(route);
      expect(slugs.filter((s: string) => s === slug), `cards for ${route}`).toHaveLength(1);
    }
    // and no card for a page that does not exist
    const expected = new Set([...routes].map(ogSlugFor));
    for (const s of slugs) expect(expected.has(s), `card ${s} has no route`).toBe(true);
  });

  it("keeps every title within 90 characters", () => {
    for (const c of OG_CARDS as { slug: string; title: string }[]) {
      expect(c.title.length, c.slug).toBeLessThanOrEqual(90);
      expect(c.title.length, c.slug).toBeGreaterThan(0);
    }
  });

  it("serves every page an og:image slug that has a card", () => {
    const docBySlug = new Map((PUBLISHED as { slug: string; canonical?: string }[]).map((d) => [`/docs/${d.slug}`, d]));
    for (const route of Object.keys(pageSource)) {
      // Base.astro computes the slug from the page's canonical path, which a doc may override.
      const path = docBySlug.get(route)?.canonical ?? route;
      expect(OG_SLUGS.has(ogSlugFor(path)), `${route} -> ${ogSlugFor(path)}`).toBe(true);
    }
  });

  it("gives no card to a doc canonicalised to another route", () => {
    for (const d of PUBLISHED as { slug: string; canonical?: string }[]) {
      if (hasOtherCanonical(d)) expect(OG_SLUGS.has(`docs-${d.slug}`), d.slug).toBe(false);
    }
  });

  it("sets each accent word once, as a whole word, in its title", () => {
    for (const c of OG_CARDS as { slug: string; title: string; accent?: string }[]) {
      if (!c.accent) continue;
      const esc = c.accent.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
      const hits = c.title.match(new RegExp(`(?<![\\p{L}\\p{N}])${esc}(?![\\p{L}\\p{N}])`, "gu")) ?? [];
      expect(hits, c.slug).toHaveLength(1);
    }
  });

  it("builds the table on features and comparison from CASES.sports", () => {
    const { tracks, dests } = CASES.sports;
    const want = {
      columns: tracks.map((t: { label: string }) => t.label),
      rows: dests.map((d: { name: string; tracks: string[] }) => ({ name: d.name, cells: tracks.map((t: { id: string }) => d.tracks.includes(t.id)) })),
    };
    for (const slug of ["features", "comparison"]) {
      const card = (OG_CARDS as { slug: string; table?: unknown }[]).find((c) => c.slug === slug);
      expect(card?.table, slug).toEqual(want);
    }
    expect(want.columns.join(" ").toLowerCase()).not.toContain("music");
  });

  it("passes the download card the install command from install.mjs", () => {
    const card = (OG_CARDS as { slug: string; command?: string }[]).find((c) => c.slug === "download");
    expect(card?.command).toBe(DOCKER_RUN);
  });

  it("gives every doc card a DOCS eyebrow and its description as the sub line", () => {
    for (const d of (PUBLISHED as { slug: string; title: string; description: string; canonical?: string }[]).filter((x) => !hasOtherCanonical(x))) {
      const card = (OG_CARDS as { slug: string; eyebrow?: string; title: string; sub?: string; visual: string }[]).find(
        (c) => c.slug === `docs-${d.slug}`,
      );
      expect(card?.eyebrow?.startsWith("DOCS · "), d.slug).toBe(true);
      expect(card?.title).toBe(d.title);
      expect(card?.sub).toBe(d.description);
      expect(card?.visual).toBe("none");
    }
  });
});

describe("public/og", () => {
  it.each(OG_CARDS.map((c: { slug: string }) => c.slug))("%s.png exists, is 1200x630 and within the size budget", (slug: string) => {
    const file = web(`public/og/${slug}.png`);
    expect(existsSync(file), file).toBe(true);
    const bytes = readFileSync(file);
    expect(bytes.subarray(1, 4).toString("latin1")).toBe("PNG");
    expect(bytes.readUInt32BE(16)).toBe(1200);
    expect(bytes.readUInt32BE(20)).toBe(630);
    expect(statSync(file).size).toBeLessThanOrEqual(MAX_BYTES);
  });

  it("has no PNG without a card", () => {
    const files = readdirSync(web("public/og")).filter((f) => f.endsWith(".png")).map((f) => f.slice(0, -4)).sort();
    expect(files).toEqual([...OG_SLUGS].sort());
  });

  it("has a manifest that matches the cards and the template", () => {
    const text = readFileSync(web("og/manifest.txt"), "utf8");
    const tpl = template();
    const want = Object.fromEntries(OG_CARDS.map((c) => [c.slug, ogHash(c, tpl)]));
    expect(parseOgManifest(text)).toEqual(want);
    // Byte for byte what the build writes, so re-running it leaves the tree clean.
    expect(text).toBe(formatOgManifest(want));
  });

  it("keeps the manifest out of the published site and free of key-and-value lines", () => {
    // public/ is copied to the site as it is; the manifest is a build record, not a page asset.
    expect(existsSync(web("public/og/manifest.json"))).toBe(false);
    // A "name: value" or "name = value" line with a long random value is what a secret scanner
    // reports. Hashes first, no separator but spaces, keeps the file from looking like one.
    expect(readFileSync(web("og/manifest.txt"), "utf8")).not.toMatch(/[:=,"]/);
  });

  it("refuses a manifest line it cannot read", () => {
    expect(() => parseOgManifest("docs-api: abc\n")).toThrow(/not a "<sha256> {2}<slug>" line/);
  });
});
