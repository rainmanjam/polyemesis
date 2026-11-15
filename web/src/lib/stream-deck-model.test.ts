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

/* The wrapped key and dial nodes' translations, recorded from the committed model (scene.ts moves
 * these nodes from here), so a re-export that moves a key fails. */
const NODE_TRANSLATIONS: Record<string, number[]> = {
  dial0: [-0.044201236218214035, 0.010747134685516357, 0.017340868711471558],
  key00: [-0.04437196999788284, 0.0676470398902893, -0.045966215431690216],
  key01: [-0.014790688641369343, 0.0676465556025505, -0.04596705734729767],
  key02: [0.01479070633649826, 0.06764611601829529, -0.045967813581228256],
  key03: [0.044372234493494034, 0.0676456019282341, -0.045968666672706604],
  key10: [-0.044111717492341995, 0.05152076482772827, -0.03227578476071358],
  key11: [-0.014703870750963688, 0.05152132362127304, -0.03227492794394493],
  key12: [0.014703827910125256, 0.05152187868952751, -0.032274071127176285],
  key13: [0.04411139711737633, 0.051522500813007355, -0.03227311372756958],
};

/** The committed model's JSON chunk. */
function readGlb(): { json: any } {
  const buf = readFileSync(join(repo, "web", "public", "models", "stream-deck-plus.glb"));
  return { json: JSON.parse(buf.subarray(20, 20 + buf.readUInt32LE(12)).toString("utf8")) };
}

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
    expect(statSync(glb).size).toBeLessThanOrEqual(1_320_000);
  });

  it("the model needs no decoder: no Draco, Meshopt or Basis, which the site's policy could not run", () => {
    const { json } = readGlb();
    for (const list of [json.extensionsUsed, json.extensionsRequired]) {
      for (const bad of ["KHR_draco_mesh_compression", "EXT_meshopt_compression", "KHR_texture_basisu"]) {
        expect(list ?? [], bad).not.toContain(bad);
      }
    }
    expect(json.extensionsRequired ?? []).toEqual(["KHR_mesh_quantization"]);
  });

  it("keeps every node and material name scene.ts looks up, and the key and dial nodes unscaled", () => {
    const { json } = readGlb();
    const names = new Map<string, any>(json.nodes.filter((n: any) => n.name).map((n: any) => [n.name, n]));
    const wanted = ["body_main", "body_stand", "dial0", ...[0, 1].flatMap((r) => [0, 1, 2, 3].map((c) => `key${r}${c}`))];
    for (const w of wanted) expect(names.has(w), w).toBe(true);
    // scene.ts moves these nodes, reads their position and adds children to them: the quantisation
    // transform must sit on an unnamed child, never on the node itself.
    for (const w of wanted.filter((n) => !n.startsWith("body"))) {
      const n = names.get(w);
      expect(n.translation, w).toEqual(NODE_TRANSLATIONS[w]);
      expect(n.scale, w).toBeUndefined();
      expect(n.mesh, w).toBeUndefined();
      expect(n.children?.length, w).toBe(1);
      expect(json.nodes[n.children[0]].mesh, w).toBeTypeOf("number");
    }
    const mats = json.materials.map((m: any) => m.name);
    for (const m of ["mat_0", "mat_7", "plate", "dialcap"]) expect(mats, m).toContain(m);
    expect(mats).toHaveLength(10);
  });

  it("README records the compression command and the SHA-256 of its input", () => {
    const readme = read(join(dir, "README.md"));
    expect(readme).toContain("bash web/scripts/stream-deck-model/compress.sh");
    expect(readme).toContain("@gltf-transform/cli@4.4.1");
    expect(readme).toContain("bbc517f71c090afe90aa0b711bc10d8875f3897f3972a578f102541a97625eff");
    for (const f of ["compress.sh", "wrap-nodes.py"]) expect(existsSync(join(dir, f)), f).toBe(true);
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
