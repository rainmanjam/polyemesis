import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { BY_FILE, SECTIONS, START_HERE } from "../data/docs.mjs";

/* The docs index: the data by value, the page by source text (rendering is
 * check-build's job). */
const page = readFileSync(join(__dirname, "..", "pages", "docs.astro"), "utf8");

describe("docs index", () => {
  it("START_HERE names four published docs", () => {
    expect(START_HERE).toHaveLength(4);
    for (const file of START_HERE) expect(BY_FILE.has(file)).toBe(true);
  });

  it("is built on InnerPage with a start-here section and collapsible groups", () => {
    expect(page).toContain("InnerPage");
    expect(page).toContain('id="start-here"');
    expect(page).toContain("<details");
  });

  it("renders the docs of every section", () => {
    expect(SECTIONS.length).toBeGreaterThan(0);
    expect(page).toContain("docsIn(");
    expect(page).toContain("SECTIONS.map");
  });

  it("carries no page-level style", () => {
    expect(page).not.toContain("<style");
  });
});
