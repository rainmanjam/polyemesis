import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

/* Source-text checks for the shared shell: the nav, the footer and the layout
 * they live in. Rendering is covered by check-build.mjs; this pins the
 * contract of the files themselves. */
const src = (p: string) => readFileSync(join(__dirname, "..", p), "utf8");
const nav = src("components/Nav.astro");
// The footer builds its repo URLs from one REPO constant; expand it so the
// assertions read the URLs a visitor gets.
const footerRaw = src("components/Footer.astro");
const repo = footerRaw.match(/const REPO = "([^"]+)"/)?.[1] ?? "";
const footer = footerRaw.replaceAll("${REPO}", repo).replace(/`([^`$]*)`/g, '"$1"');
const base = src("layouts/Base.astro");

/** Every .astro file under a directory. */
const astroFiles = (dir: string): string[] =>
  readdirSync(dir).flatMap((f) => {
    const p = join(dir, f);
    return statSync(p).isDirectory() ? astroFiles(p) : p.endsWith(".astro") ? [p] : [];
  });

describe("Nav", () => {
  it("lists exactly Features, Docs and Install as internal links, in both lists", () => {
    const hrefs = [...nav.matchAll(/href[:=]\s*["'](\/[^"']*)["']/g)].map((m) => m[1]);
    // The link array is rendered into the desktop row and the mobile menu, so
    // the source names each href once; the Install button is written twice.
    expect(new Set(hrefs)).toEqual(new Set(["/", "/features", "/docs", "/download"]));
    expect(nav).not.toContain("/calculator");
    expect(nav).not.toContain("/comparison");
  });

  it("renders the install link as a ghost button in both places", () => {
    const installs = nav.match(/href="\/download"[^>]*btn btn-ghost|btn btn-ghost[^>]*href="\/download"/g) ?? [];
    expect(installs.length).toBe(2);
    expect(nav.match(/Install polyemesis/g)?.length).toBe(2);
  });

  it("marks text links nav-link and the Install button nav-install, with the current-page rules in inner.css", () => {
    expect(nav.match(/nav-link/g)?.length).toBeGreaterThanOrEqual(3);
    expect(nav.match(/nav-install/g)?.length).toBe(2);
    expect(nav).toContain("aria-current");
    const css = src("styles/inner.css");
    expect(css).toMatch(/\.nav-link\[aria-current="page"\]\s*\{[^}]*var\(--color-fg\)/);
    expect(css).toMatch(/\.nav-install\[aria-current="page"\]\s*\{[^}]*inset 0 -2px 0 var\(--color-accent\)/);
  });

  it("has no border under it", () => {
    const header = nav.slice(nav.indexOf("<header"), nav.indexOf(">", nav.indexOf("<header")));
    expect(header).not.toMatch(/border-b/);
  });
});

describe("Footer", () => {
  const hrefs = [
    "/features", "/comparison", "/multistream", "/free-restream-service", "/twitch-vod-track",
    "/download", "/calculator", "/docs", "/docs/audio-routing", "/how-to-multistream-from-obs",
    "/docs/obs", "/docs/api", "https://github.com/rainmanjam/polyemesis",
    "https://github.com/rainmanjam/polyemesis/issues", "https://hub.docker.com/r/rainmanjam/polyemesis",
    "https://github.com/rainmanjam/polyemesis/blob/main/LICENSE",
    "https://github.com/rainmanjam/polyemesis/blob/main/CHANGELOG.md",
  ];
  it.each(hrefs)("links %s", (h) => {
    expect(footer).toContain(`"${h}"`);
  });

  it("carries the trademark sentence verbatim", () => {
    expect(footer).toContain(
      "polyemesis is not affiliated with or endorsed by YouTube, Twitch, Kick, Rumble, Facebook, Meta, X or Elgato; their names and logos are trademarks of their owners.",
    );
  });

  it("reads the version from the changelog and prints nothing without it", () => {
    expect(footer).toContain("latestRelease");
    expect(footer).toMatch(/release && \(/);
  });
});

describe("Base layout", () => {
  const props = base.slice(base.indexOf("interface Props"), base.indexOf("}\n", base.indexOf("interface Props")));
  it("has no grid or announce prop and no grid layer", () => {
    expect(props).not.toMatch(/\bgrid\b|announce/);
    expect(base).not.toContain("page-grid");
  });

  it("is not given a grid attribute by any page", () => {
    const offenders = astroFiles(join(__dirname, "..", "pages")).filter((f) => {
      const t = readFileSync(f, "utf8");
      return /<Base\b[^>]*?(\s|^)grid(\s|>|=)/s.test(t);
    });
    expect(offenders).toEqual([]);
  });
});
