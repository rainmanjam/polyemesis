import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const read = (p: string) => readFileSync(new URL(p, import.meta.url), "utf8");
const page = read("../layouts/DocPage.astro");
const route = read("../pages/docs/[slug].astro");

describe("DocPage", () => {
  it.each(["data-doc-column", 'class="doc-prose', "data-pager", "card-grid--2", 'variant="rail"', 'variant="phone"', "code-copy.ts", "mermaid-render.ts", "bootInner"])(
    "contains %s",
    (s) => expect(page).toContain(s),
  );
});

describe("docs/[slug].astro", () => {
  it("passes headings read from the markdown", () => {
    expect(route).toContain("extractHeadings");
    expect(route).toContain("headings=");
  });
});
