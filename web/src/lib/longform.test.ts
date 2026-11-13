import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/* The four long-form search pages sit on the shared shell. Source-text checks:
 * rendering and the copy-kept rules belong to check-build. */
type Legacy = Record<string, { h1: string; h2: string[]; text: string }>;

const fixture = JSON.parse(
  readFileSync(join(__dirname, "legacy-inner.fixture.json"), "utf8"),
) as Legacy;

const PAGES = [
  ["/multistream", "multistream.astro"],
  ["/free-restream-service", "free-restream-service.astro"],
  ["/how-to-multistream-from-obs", "how-to-multistream-from-obs.astro"],
  ["/twitch-vod-track", "twitch-vod-track.astro"],
] as const;

const page = (file: string) => readFileSync(join(__dirname, "..", "pages", file), "utf8");
const squash = (s: string) => s.replace(/\s+/g, " ").trim();

describe.each(PAGES)("%s on the inner-page shell", (route, file) => {
  const src = page(file);

  it("uses InnerPage with the longform typography and no page-level style or old trail", () => {
    expect(src).toContain("InnerPage");
    expect(src).toMatch(/\blongform\b/);
    expect(src).not.toContain("<style");
    expect(src).not.toContain("Breadcrumbs");
  });

  it("labels the primary action Install polyemesis, never Install it or a bare Install", () => {
    expect(src).not.toMatch(/>\s*Install( it)?\s*</);
  });

  it("keeps its path and the frozen h1", () => {
    expect(src).toContain(`path="${route}"`);
    expect(squash(src)).toContain(fixture[route].h1.replace(/\s+/g, " "));
  });

  it("gives every index entry a section id that exists", () => {
    const ids = [...src.matchAll(/id="([a-z0-9-]+)"/g)].map((m) => m[1]);
    const listed = [...src.matchAll(/\{ id: "([a-z0-9-]+)"/g)].map((m) => m[1]);
    expect(listed.length).toBeGreaterThanOrEqual(3);
    for (const id of listed) expect(ids).toContain(id);
  });
});

describe("page specifics", () => {
  it("twitch-vod-track reads as a feature, not a way around a claim", () => {
    const src = page("twitch-vod-track.astro");
    for (const word of ["bypass", "dodge", "DMCA", "avoid a claim", "get around"]) {
      expect(src).not.toContain(word);
    }
  });

  it("how-to keeps its HowTo JSON-LD, its section ids and the checked date", () => {
    const src = page("how-to-multistream-from-obs.astro");
    expect(src).toContain('"@type": "HowTo"');
    for (const id of ["routes", "walkthrough", "gotchas"]) expect(src).toContain(`id="${id}"`);
    expect(src).toContain('const CHECKED = "2026-08-15"');
  });

  it("free-restream-service keeps the-trade and the checked date", () => {
    const src = page("free-restream-service.astro");
    expect(src).toContain('id="the-trade"');
    expect(src).toContain('const CHECKED = "2026-08-15"');
  });

  it("multistream keeps its docs links", () => {
    const src = page("multistream.astro");
    expect(src).toContain("/docs/audio-routing");
    expect(src).toContain("/docs/platforms");
  });
});
