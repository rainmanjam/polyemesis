/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

/* GRADIENTS APPEAR ONLY IN THE LOGO MARK (docs/DESIGN-SYSTEM.md, Colour).
 *
 * The 2026-11 review found two in the console that broke it: a blue area fade
 * under the Monitoring bitrate chart and a continuous colour ramp on the audio
 * meter. Both are flat now. This holds the line for every source file, so a
 * new fade has to argue its way onto the allow-list below rather than arrive
 * unnoticed.
 *
 * Comments are stripped first, so explaining the rule does not trip it. */

const SRC = fileURLToPath(new URL("../", import.meta.url)); // ui/src/

/** Exact strings that are patterns, not fades, each with its file and why.
 *  Only the string is excused, never the whole file: a new fade added beside
 *  one of these still fails. */
const ALLOWED: Record<string, string[]> = {
  // The transparency checkerboard behind the overlay preview: four
  // linear-gradient layers with hard 25%/75% stops, i.e. a tiled pattern.
  "components/OverlaySettings.tsx": [
    "linear-gradient(45deg,#374151 25%,transparent 25%),linear-gradient(-45deg,#374151 25%,transparent 25%),linear-gradient(45deg,transparent 75%,#374151 75%),linear-gradient(-45deg,transparent 75%,#374151 75%)",
  ],
};

function walk(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (/\.(tsx?|css)$/.test(name) && !/\.test\.tsx?$/.test(name)) out.push(p);
  }
  return out;
}

const GRADIENT =
  /createLinearGradient|createRadialGradient|createConicGradient|<linearGradient|<radialGradient|(?:linear|radial|conic)-gradient\(|\bbg-(?:linear|gradient|radial|conic)\b/;

describe("no gradients outside the logo mark", () => {
  const files = walk(SRC);

  it("scans the sources", () => {
    expect(files.length).toBeGreaterThan(100);
  });

  it("finds no fade in any component, page or stylesheet", () => {
    const offenders = files
      .map((f) => relative(SRC, f))
      .filter((rel) => {
        let code = readFileSync(join(SRC, rel), "utf8")
          .replace(/\/\*[\s\S]*?\*\//g, "")
          .replace(/(^|[^:])\/\/.*$/gm, "$1");
        for (const ok of ALLOWED[rel] ?? []) code = code.split(ok).join("");
        return GRADIENT.test(code);
      });
    expect(offenders).toEqual([]);
  });

  it("still finds every excused pattern, so a stale excuse cannot linger", () => {
    for (const [rel, oks] of Object.entries(ALLOWED)) {
      const code = readFileSync(join(SRC, rel), "utf8");
      for (const ok of oks) expect(code, rel).toContain(ok);
    }
  });
});
