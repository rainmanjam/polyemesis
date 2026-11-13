import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";

/* Source-level rules for the inner pages' copy and styling hooks. "one
 * maintainer" is guarded site-wide by no-one-maintainer.test.ts. The docs
 * (docs/*.md) are not covered: they keep the word "pre-release" where it is
 * true, and /comparison uses the {release} token instead. */

const SRC = new URL("../", import.meta.url).pathname;

function filesUnder(dir: string, match = /\.(astro|mjs)$/): string[] {
  const out: string[] = [];
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) out.push(...filesUnder(p, match));
    else if (match.test(e.name)) out.push(p);
  }
  return out;
}

const pages = filesUnder(join(SRC, "pages")).filter((f) => !/pages\/index\.astro$/.test(f));
const FILES = [
  ...pages,
  ...filesUnder(join(SRC, "components/inner")),
  join(SRC, "layouts/InnerPage.astro"),
  join(SRC, "layouts/VsPage.astro"),
  join(SRC, "data/features.mjs"),
  join(SRC, "data/install-page.mjs"),
  join(SRC, "data/vs-index.mjs"),
  ...filesUnder(join(SRC, "data/vs")),
  ...filesUnder(join(SRC, "scripts"), /(?<!\.test)\.ts$/),
];
const SOURCES = FILES.map((f) => ({ name: relative(SRC, f), text: readFileSync(f, "utf8") }));

function offenders(re: RegExp): string[] {
  return SOURCES.filter((s) => re.test(s.text)).map((s) => s.name);
}

describe("inner page sources", () => {
  it("reads the pages it is meant to guard", () => {
    expect(pages.length).toBeGreaterThanOrEqual(14);
    expect(SOURCES.length).toBeGreaterThanOrEqual(25);
    const names = SOURCES.map((s) => s.name);
    expect(names.filter((n) => /^data\/vs\/[^/]+\.mjs$/.test(n)).length).toBeGreaterThanOrEqual(5);
    expect(names).toContain("scripts/inner.ts");
    expect(names.some((n) => n.endsWith(".test.ts"))).toBe(false);
  });

  it("uses none of the banned words, says Stream Deck + with its space, and quotes no star counts", () => {
    expect(offenders(/bypass|dodge|DMCA|pre-release|Stream Deck\+|\b\d+\s+stars?\b/i)).toEqual([]);
  });

  it("never sets text in the faint colour", () => {
    expect(offenders(/text-faint|--color-faint/)).toEqual([]);
  });

  it("has no entrance-animation hooks", () => {
    expect(offenders(/class="reveal"|data-reveal/)).toEqual([]);
  });

  it('puts "where it loses" in no heading, lede or description', () => {
    const bad: string[] = [];
    for (const s of SOURCES) {
      for (const m of s.text.matchAll(/\b(?:heading|lede|description)=(?:"([^"]*)"|\{([^}]*)\})/g)) {
        if (/where it loses/i.test(m[1] ?? m[2] ?? "")) bad.push(s.name);
      }
    }
    expect(bad).toEqual([]);
  });
});
