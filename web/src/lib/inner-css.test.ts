import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("../styles/inner.css", import.meta.url), "utf8");

/** The declaration block of the first rule whose selector list contains `selector`. */
function ruleFor(selector: string): string {
  const re = /([^{}]+)\{([^{}]*)\}/g;
  const bare = css.replace(/\/\*[\s\S]*?\*\//g, "");
  for (const m of bare.matchAll(re)) {
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
    expect(ruleFor(".doc-table")).not.toContain("--radius-token");
  });
  it("never lets inline code fall under 12px", () => {
    expect(ruleFor(".doc-prose code:not(pre code)")).toMatch(/font-size: max\(/);
  });
  it("has no .reveal rule", () => {
    expect(css).not.toContain(".reveal");
  });
});
