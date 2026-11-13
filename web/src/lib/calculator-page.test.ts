import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const calc = readFileSync(new URL("../pages/calculator.astro", import.meta.url), "utf8");
const notFound = readFileSync(new URL("../pages/404.astro", import.meta.url), "utf8");

const IDS = [
  "mode-simple", "mode-verbose", "hours", "per-week", "platforms", "custom-list", "custom-name",
  "custom-kbps", "custom-add", "video-kbps", "video-hint", "audio-kbps", "tracks", "overhead",
  "headroom", "r-ingest", "r-ingest-note", "r-egress", "r-egress-note", "r-per", "r-week",
  "r-month", "warnings",
];

describe("/calculator", () => {
  it.each(IDS)("keeps the id %s", (id) => {
    expect(calc).toContain(`id="${id}"`);
  });
  it("keeps data-id, data-min and data-max adjacent in that order", () => {
    expect(calc).toMatch(/data-id=\{p\.id\}\s+data-min=\{p\.min\}\s+data-max=\{p\.max\}/);
  });
  it("keeps the checked date and the calc module", () => {
    expect(calc).toContain("checked {CHECKED}");
    expect(calc).toContain('from "../lib/calc"');
  });
  it("sits on InnerPage, not Base", () => {
    expect(calc).toContain("InnerPage");
    expect(calc).not.toMatch(/import Base\b/);
  });
  it("has no entrance animation, no card class and no coral pressed state", () => {
    expect(calc).not.toMatch(/\brise\b/);
    expect(calc).not.toMatch(/class="[^"]*\bcard\b/);
    expect(calc).not.toMatch(/btn-primary/);
  });
  it("has no text under 12px", () => {
    expect(calc).not.toMatch(/text-\[(?:[0-9]|1[01])px\]/);
  });
  it("has no PageIndex and no page-level style", () => {
    expect(calc).not.toContain("PageIndex");
    expect(calc).not.toContain("<style");
  });
});

describe("/404", () => {
  it("is a noindex InnerPage at /404", () => {
    expect(notFound).toContain("InnerPage");
    expect(notFound).toContain("noindex");
    expect(notFound).toContain('path="/404"');
  });
  it("links home, the install page and the docs", () => {
    for (const href of ['href="/"', 'href="/download"', 'href="/docs"']) expect(notFound).toContain(href);
  });
  it("has no style block, no index and no band", () => {
    expect(notFound).not.toContain("<style");
    expect(notFound).not.toContain("PageIndex");
    expect(notFound).not.toMatch(/band=/);
  });
});
