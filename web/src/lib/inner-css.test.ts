import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// Comments stripped, so a selector named only in a comment does not count.
const css = readFileSync(new URL("../styles/inner.css", import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

/** The declaration block of the first rule whose selector list contains `selector`. */
function ruleFor(selector: string): string {
  const re = /([^{}]+)\{([^{}]*)\}/g;
  for (const m of css.matchAll(re)) {
    if (m[1].split(",").some((s) => s.trim() === selector)) return m[2];
  }
  return "";
}

describe("inner.css docs article rules", () => {
  it("sets the article body at 17px", () => {
    expect(ruleFor(".doc-prose")).toContain("font-size: 17px");
  });
  it("caps the running text measure at 39rem", () => {
    expect(css).toContain("max-width: 39rem");
  });
  it.each([
    ".doc-prose h4",
    ".doc-prose ol",
    ".doc-prose ol ol",
    ".doc-prose li > ul",
    ".doc-prose > table",
    ".doc-prose [data-mermaid]",
    '[data-mermaid="failed"]',
    ".doc-table",
    'input[type="checkbox"]',
  ])("has a rule for %s", (sel) => {
    expect(css).toContain(sel);
  });
  it("keeps table corners within 4px", () => {
    const radius = /border-radius:\s*([\d.]+)px/.exec(ruleFor(".doc-table"));
    expect(radius).not.toBeNull();
    expect(Number(radius?.[1])).toBeLessThanOrEqual(4);
  });
  it("never lets inline code fall under 12px", () => {
    expect(ruleFor(".doc-prose code:not(pre code)")).toMatch(/font-size: max\(/);
  });
  it("has no .reveal rule", () => {
    expect(css).not.toContain(".reveal");
  });
});

describe("inner.css collisions between areas", () => {
  it("defines the section heading size and the section gap once", () => {
    expect(css.match(/--h2-section:/g)).toHaveLength(1);
    expect(css.match(/--section-gap:/g)).toHaveLength(1);
    expect(css).not.toMatch(/\.(fe|inst|cp-sec|vs-sec) h2 \{[^}]*font-size: clamp/);
  });
  it("scopes the comparison table's look to .cmp-wrap, so docs tables keep the docs rules", () => {
    expect(css).not.toMatch(/^\s*\.cmp (td|th|thead|tbody|\[)/m);
    expect(css).toContain(":where(.cmp-wrap) .cmp td { color: var(--color-muted); }");
  });
  it("writes the long-form element defaults at zero specificity, so any class wins", () => {
    expect(css).toContain(":where(.longform) :where(p, ul, ol)");
    expect(css).not.toMatch(/^\s*\.longform (p|ul|ol)\b/m);
  });
  it("gives focusable controls the anchor offset as a scroll margin", () => {
    expect(css).toMatch(/:where\(\.inner-main, \[data-doc-column\], \.docnav, nav\.rail\)\s+:where\(a, button, input, select, textarea, summary, \[tabindex\]\) \{\s+scroll-margin-top: var\(--anchor-offset\)/);
  });
  it("uses ink, never coral, for the /docs entry hover and the /download steps and next links", () => {
    expect(ruleFor(".dl a:hover .dl-t b")).toContain("var(--color-fg)");
    expect(ruleFor(":where(.inner-main) .inst-steps > li::before")).toContain("var(--color-fg)");
    expect(ruleFor(":where(.inner-main) .inst-next a:hover b")).not.toContain("accent");
  });
});
