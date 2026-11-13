import { existsSync, readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { VS_INDEX } from "../data/vs-index.mjs";

const fixture = JSON.parse(
  readFileSync(new URL("./legacy-inner.fixture.json", import.meta.url), "utf8"),
) as Record<string, { text: string }>;

const norm = (s: string) =>
  s
    .replace(/[‐-―−]/g, "-")
    .replace(/[‘’]/g, "'")
    .replace(/[“”]/g, '"')
    .replace(/\s+/g, " ")
    .trim()
    .toLowerCase();

const legacy = norm(fixture["/comparison"].text);

describe("VS_INDEX", () => {
  it("has the five one-at-a-time entries", () => {
    expect(VS_INDEX.map((e) => e.slug)).toEqual([
      "aitum-multistream",
      "obs-multi-rtmp",
      "restreamer",
      "streamelements",
      "streamlabs",
    ]);
  });

  it.each(VS_INDEX)("$slug links to an existing page", (e) => {
    expect(e.href).toBe(`/vs/${e.slug}`);
    expect(existsSync(new URL(`../pages/vs/${e.slug}.astro`, import.meta.url))).toBe(true);
  });

  it.each(VS_INDEX)("$slug copy is the old page's copy", (e) => {
    for (const s of [e.name, e.kind, e.line]) expect(legacy).toContain(norm(s));
  });
});

describe("comparison.astro source", () => {
  const src = readFileSync(new URL("../pages/comparison.astro", import.meta.url), "utf8");
  it("links the matrix on the homepage", () => {
    expect(src).toContain('href="/#matrix"');
    expect(src).not.toContain("/features#matrix");
  });
  it("states maturity through the release token", () => {
    expect(src).not.toContain("pre-release");
    expect(src).toContain("fillRelease");
  });
  it("leads the header with the install action", () => {
    expect(src).toMatch(/slot="actions" class="btn btn-primary" href="\/download">Install polyemesis</);
    expect(src.indexOf("Install polyemesis")).toBeLessThan(src.indexOf("See the table"));
  });
  it("explains the Filter only cell", () => {
    expect(src).toContain("“Filter only” for Restreamer");
  });
  it("uses the shared table for both tables", () => {
    expect(src).toContain('firstHeader="Capability"');
    expect(src).toContain("rows={wins}");
    expect(src).toContain("rows={shared}");
  });
});
