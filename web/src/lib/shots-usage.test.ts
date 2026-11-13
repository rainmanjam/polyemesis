import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/* Every screenshot kept in the site folder is used by something under web/src.
 * A capture nothing references is dead weight in the repository and in every
 * clone; docs/media/ keeps the originals, so deleting one here loses nothing. */

const SRC = new URL("../", import.meta.url).pathname;
const SHOTS = join(SRC, "assets", "shots");

/** Kept without a user on purpose. Plan 4 removes an entry when it uses the file. */
const WHITELIST = ["06-renditions.png"];

function sourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (p === SHOTS) continue;
    if (e.isDirectory()) out.push(...sourceFiles(p));
    else if (!/\.test\.[a-z]+$/.test(e.name) && !/\.(png|jpe?g|webp|avif|woff2?|glb)$/.test(e.name)) out.push(p);
  }
  return out;
}

describe("screenshots in src/assets/shots", () => {
  const shots = readdirSync(SHOTS).filter((f) => f.endsWith(".png"));
  const sources = sourceFiles(SRC).map((f) => readFileSync(f, "utf8"));

  it("finds the folder and the sources it searches", () => {
    expect(shots.length).toBeGreaterThanOrEqual(8);
    expect(sources.length).toBeGreaterThanOrEqual(100);
  });

  it("every capture is referenced by file name, or whitelisted", () => {
    const unused = shots.filter((s) => !WHITELIST.includes(s) && !sources.some((t) => t.includes(s)));
    expect(unused).toEqual([]);
  });

  it("every whitelist entry is a file that exists", () => {
    for (const w of WHITELIST) expect(shots).toContain(w);
  });
});
