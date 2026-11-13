import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/* The whole-file match: the phrase wrapped across a line break ("one\nmaintainer") is still the phrase. */
const PHRASE = /\bone[\s-]+maintainer/i;
const ROOT = new URL("../../../", import.meta.url).pathname;

/* The only files allowed to hold the phrase: this guard, and the tests that list it as a banned phrase. Named one by
 * one, so a new test file is scanned. An entry whose file no longer holds the phrase fails, so the list cannot go stale. */
const HOLD_THE_PHRASE = [
  "web/src/lib/no-one-maintainer.test.ts",
  "web/src/lib/inner-copy.test.ts",
  "web/src/lib/home-copy.test.ts",
  "web/src/lib/home-sections.test.ts",
  "web/src/lib/audiences.test.ts",
  "web/src/lib/features-data.test.ts",
];

function filesUnder(dir: string, keep: (name: string) => boolean): string[] {
  const out: string[] = [];
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) out.push(...filesUnder(p, keep));
    else if (keep(e.name)) out.push(p);
  }
  return out;
}

const md = (name: string) => name.endsWith(".md");

function publicFiles(): string[] {
  return [
    ...filesUnder(join(ROOT, "web/src"), () => true),
    ...filesUnder(join(ROOT, "docs"), md),
    ...filesUnder(join(ROOT, "integrations"), (n) => n === "README.md"),
    join(ROOT, "README.md"),
    join(ROOT, "CHANGELOG.md"),
  ];
}

function read(f: string): string {
  try {
    return readFileSync(f, "utf8");
  } catch {
    return "";
  }
}

describe("retired phrase", () => {
  const files = publicFiles();

  it("scans the folders it is meant to", () => {
    expect(files.length).toBeGreaterThanOrEqual(100);
    for (const must of ["docs/API.md", "CHANGELOG.md", "README.md", "integrations/streamdeck/README.md"]) {
      expect(files.map((f) => f.replace(ROOT, ""))).toContain(must);
    }
    // Docs in a sub-folder are covered too.
    expect(files.some((f) => /\/docs\/[^/]+\/.+\.md$/.test(f))).toBe(true);
  });

  it('no "one maintainer" anywhere public, even wrapped across a line', () => {
    const hits = files.map((f) => f.replace(ROOT, "")).filter((f) => !HOLD_THE_PHRASE.includes(f) && PHRASE.test(read(join(ROOT, f))));
    expect(hits).toEqual([]);
  });

  it("the files exempt from the scan still hold the phrase (the list is not stale)", () => {
    const stale = HOLD_THE_PHRASE.filter((f) => !PHRASE.test(read(join(ROOT, f))));
    expect(stale).toEqual([]);
  });

  it("matches the phrase when it is wrapped, hyphenated or in capitals", () => {
    for (const s of ["one\nmaintainer", "one-maintainer", "One   Maintainer", "one\r\n  maintainer"]) expect(PHRASE.test(s)).toBe(true);
    for (const s of ["someone maintainer", "one of the maintainers", "none maintainer"]) expect(PHRASE.test(s)).toBe(false);
  });
});
