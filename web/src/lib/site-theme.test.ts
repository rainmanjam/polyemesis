import { readFileSync, readdirSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

import { describe, expect, it } from "vitest";

/* Source-level guards for the site token layer and its two display faces. */

const here = dirname(fileURLToPath(import.meta.url));
const web = join(here, "..", "..");
const read = (...p: string[]) => readFileSync(join(web, ...p), "utf8");

describe("the site token layer (2026-11 redesign)", () => {
  const css = read("src", "styles", "global.css");
  const base = read("src", "layouts", "Base.astro");

  it.each([
    '--font-display: "Bricolage Grotesque", ui-sans-serif, system-ui, sans-serif;',
    '--font-accent: "Instrument Serif", serif;',
    "--color-ink: #07080a;",
    "--color-surface: #0c0e12;",
    "--color-raised: #11141a;",
    "--color-line: #1f2228;",
    "--color-line-strong: #2a2e36;",
    "--color-fg: #ece9e2;",
    "--color-muted: #8b8f97;",
    "--color-faint: #4a4e56;",
    "--color-accent: #ff5a3c;",
    "--color-accent-ink: #120503;",
  ])("declares %s", (decl) => {
    expect(css).toContain(decl);
  });

  it("has no --home-bone", () => {
    expect(css).not.toContain("--home-bone");
    expect(base).not.toContain("--home-bone");
  });

  it("preloads the display face and leaves the italic serif to swap in", () => {
    expect(base).toMatch(/rel="preload"[^>]*\/fonts\/bricolage-grotesque\.woff2/);
    expect(base).not.toMatch(/rel="preload"[^>]*instrument-serif-italic/);
  });

  it("declares both new faces", () => {
    expect(base).toMatch(/font-family: "Bricolage Grotesque";[^}]*font-weight: 200 800;/);
    expect(base).toMatch(/font-family: "Instrument Serif";[^}]*font-style: italic;/);
  });

  it("paints the browser chrome with the site ground", () => {
    expect(base).toContain('<meta name="theme-color" content="#07080a"');
  });

  it("ships exactly four font files within budget", () => {
    const dir = join(web, "public", "fonts");
    expect(readdirSync(dir).sort()).toEqual([
      "bricolage-grotesque.woff2",
      "ibm-plex-sans.woff2",
      "instrument-serif-italic.woff2",
      "jetbrains-mono.woff2",
    ]);
    expect(statSync(join(dir, "bricolage-grotesque.woff2")).size).toBeLessThanOrEqual(80_000);
    expect(statSync(join(dir, "instrument-serif-italic.woff2")).size).toBeLessThanOrEqual(45_000);
  });

  it("no longer names the retired faces anywhere in src or public", () => {
    const hits: string[] = [];
    const walk = (d: string) => {
      for (const e of readdirSync(d, { withFileTypes: true })) {
        const p = join(d, e.name);
        if (e.isDirectory()) walk(p);
        else if (!/\.(woff2|png|jpe?g|webp|ico|mp4|webm)$/.test(e.name) && p !== fileURLToPath(import.meta.url)) {
          const t = readFileSync(p, "utf8");
          if (t.includes("Space Grotesk") || t.includes("IBM Plex Serif")) hits.push(p);
        }
      }
    };
    walk(join(web, "src"));
    walk(join(web, "public"));
    expect(hits).toEqual([]);
  });

  it("records both licences", () => {
    const lic = read("public", "third-party-licenses.txt");
    expect(lic).toContain("Bricolage Grotesque");
    expect(lic).toContain("Instrument Serif");
    expect(lic).toContain("SIL OPEN FONT LICENSE Version 1.1");
  });

  it("has a licence entry naming every shipped font file", () => {
    const lic = read("public", "third-party-licenses.txt");
    const base = read("src", "layouts", "Base.astro");
    const referenced = [...base.matchAll(/\/fonts\/([\w.-]+\.woff2)/g)].map((m) => m[1]);
    const shipped = readdirSync(join(web, "public", "fonts"));
    const files = [...new Set([...referenced, ...shipped])];
    expect(files.length).toBeGreaterThanOrEqual(4);
    for (const f of files) expect(lic, `no licence entry names /fonts/${f}`).toContain(`/fonts/${f}`);
  });
});
