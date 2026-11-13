import { execFileSync } from "node:child_process";
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

/* Filesystem guards for the Stream Deck + model scripts. The model derives from
 * a manufacturer's CAD file, so what may be committed is strict: the scripts and
 * the face rectangles, never the STP or the intermediate deck.glb. */

const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, "..", "..", "..");
const dir = join(repo, "web", "scripts", "stream-deck-model");
const read = (p: string) => readFileSync(p, "utf8");

const REGEN =
  "blender -b --python web/scripts/stream-deck-model/build.py -- web/scripts/stream-deck-model/work/deck.glb web/scripts/stream-deck-model/faces.json web/public/models/stream-deck-plus.glb 2";

function walk(root: string): string[] {
  const out: string[] = [];
  for (const e of readdirSync(root, { withFileTypes: true })) {
    const p = join(root, e.name);
    if (e.isDirectory()) {
      if (p === join(dir, "work")) continue;
      out.push(...walk(p));
    } else out.push(p);
  }
  return out;
}

describe("stream-deck-model scripts", () => {
  it("holds the build, the faces, the regeneration script and the README", () => {
    for (const f of ["build.py", "faces.json", "regen.sh", "README.md"]) {
      expect(existsSync(join(dir, f)), f).toBe(true);
    }
  });

  it("README states where the model comes from and what is not recorded", () => {
    const readme = read(join(dir, "README.md"));
    for (const s of ["Stream Deck + STP", "Elgato Marketplace", "not in the repo", "not recorded", REGEN]) {
      expect(readme, s).toContain(s);
    }
    expect(readme).not.toMatch(/Stream Deck\+/);
  });

  it("holds no STP, STEP or deck.glb outside work/, and every file is small", () => {
    for (const p of walk(dir)) {
      const rel = relative(dir, p);
      expect(rel, rel).not.toMatch(/\.(glb|stp|step)$/i);
      expect(statSync(p).size, rel).toBeLessThanOrEqual(100_000);
    }
  });

  it("git ignores the CAD inputs at every depth and keeps the scripts", () => {
    const ignored = (p: string) => {
      try {
        execFileSync("git", ["check-ignore", "-q", "--", p], { cwd: repo });
        return true;
      } catch {
        return false;
      }
    };
    const base = "web/scripts/stream-deck-model/";
    for (const p of ["work/deck.glb", "deck.glb", "probes/x.glb", "anything.stp", "work/a/b.step"]) {
      expect(ignored(base + p), p).toBe(true);
    }
    for (const p of ["build.py", "faces.json", "regen.sh", "README.md", "probes/bl_top.py"]) {
      expect(ignored(base + p), p).toBe(false);
    }
  });

  it("the committed model exists and stays small", () => {
    const glb = join(repo, "web", "public", "models", "stream-deck-plus.glb");
    expect(existsSync(glb)).toBe(true);
    expect(statSync(glb).size).toBeLessThanOrEqual(3_500_000);
  });

  it("build.py is not named build2 and documents the real command", () => {
    const first = read(join(dir, "build.py")).split("\n")[0];
    expect(first).not.toContain("build2");
    expect(first).toContain("blender -b --python web/scripts/stream-deck-model/build.py -- in.glb faces.json out.glb [minmm]");
  });

  it("faces.json has exactly two rows of four keys, the strip and four dials", () => {
    const keys = Object.keys(JSON.parse(read(join(dir, "faces.json")))).sort((a, b) => a.localeCompare(b));
    const want = ["k00", "k01", "k02", "k03", "k10", "k11", "k12", "k13", "strip", "dial0", "dial1", "dial2", "dial3"];
    expect(keys).toEqual([...want].sort((a, b) => a.localeCompare(b)));
  });
});
