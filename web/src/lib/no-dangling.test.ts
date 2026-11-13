import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";

/* The old /features components were deleted when the page was rebuilt. This
 * keeps them, and the names that pointed at them, from coming back. */

const WEB = new URL("../../", import.meta.url).pathname;
const RETIRED = ["Breadcrumbs.astro", "SpecStrip.astro", "MixMatrix.astro", "LoudnessBars.astro"];
const NAMES = /Breadcrumbs|SpecStrip|MixMatrix\.astro|LoudnessBars|data-room-routing/;

function filesUnder(dir: string): string[] {
  const out: string[] = [];
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) out.push(...filesUnder(p));
    else if (/\.(astro|ts|mjs|css|md|mdx)$/.test(e.name) && !/\.test\.[a-z]+$/.test(e.name)) out.push(p);
  }
  return out;
}

describe("retired components", () => {
  it("the four files do not exist", () => {
    for (const f of RETIRED) expect(existsSync(join(WEB, "src/components", f)), f).toBe(false);
  });

  it("nothing under src or scripts names them, except features.astro for data-room-routing", () => {
    const files = [...filesUnder(join(WEB, "src")), ...filesUnder(join(WEB, "scripts"))];
    expect(files.length).toBeGreaterThanOrEqual(100);
    const hits: string[] = [];
    for (const f of files) {
      const rel = relative(WEB, f);
      const text = readFileSync(f, "utf8");
      const names = rel === "src/pages/features.astro" ? /Breadcrumbs|SpecStrip|MixMatrix\.astro|LoudnessBars/ : NAMES;
      const m = names.exec(text);
      if (m) hits.push(`${rel}: ${m[0]}`);
    }
    expect(hits).toEqual([]);
  });

  it("the track layout the features page needs is still imported by it", () => {
    expect(existsSync(join(WEB, "src/data/tracks.mjs"))).toBe(true);
    expect(readFileSync(join(WEB, "src/pages/features.astro"), "utf8")).toMatch(/from "\.\.\/data\/tracks\.mjs"/);
  });
});
