import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

import { describe, expect, it } from "vitest";

/* Source-text checks, like features-shots.test.ts: importing an .astro module
 * from vitest would drag in the whole Astro pipeline to answer a question about
 * a string. The behaviour (the enlarge dialog) is exercised by the page checks;
 * this pins the parts that must not drift. */
const here = dirname(fileURLToPath(import.meta.url));
const shot = readFileSync(join(here, "..", "components", "inner", "Shot.astro"), "utf8");

describe("Shot.astro", () => {
  it("wraps the picture in the enlarge button the Lightbox listens for", () => {
    expect(shot).toContain('class="shot-open"');
    expect(shot).toContain("data-shot=");
    expect(shot).toContain("data-shot-alt=");
    expect(shot).toContain("aria-label={`Expand screenshot: ${alt}`}");
  });

  it("paints the placeholder and loads lazily by default", () => {
    expect(shot).toContain("lqipStyle");
    expect(shot).toContain("loading=");
    expect(shot).toMatch(/lazy\s*=\s*true/);
  });

  it("never hard-codes eager loading", () => {
    expect(shot).not.toContain('loading="eager"');
  });

  it("says eager out loud when asked, because Astro turns an undefined loading into lazy", () => {
    expect(shot).toContain('lazy ? "lazy" : "eager"');
  });

  it("uses the shared frame classes, not a stylesheet of its own", () => {
    expect(shot).toContain('<figure class="frame">');
    expect(shot).toContain('<div class="frame-bar">');
    expect(shot).not.toMatch(/<style/);
  });

  it("names no screenshot file, so a page's props decide which one", () => {
    expect(shot).not.toMatch(/\b\d{2}-[a-z0-9-]+\.png\b/);
  });

  it("fails the build for a screenshot that is not in src/assets/shots", () => {
    expect(shot).toContain("Shot.astro references ../../assets/shots/${name}, which does not exist");
  });
});
