// node --test scripts/check-shots.test.mjs
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, utimesSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { missingOrStale, usersOf, withoutComments } from "./check-shots.mjs";

const manifest = {
  shots: [
    { file: "a.png", scene: "sports" },
    { file: "b.png", scene: "sports" },
    { file: "c.png", scene: "full" },
  ],
};
const SINCE = 1000;
const statFrom = (table) => (p) => {
  const name = p.split("/").pop();
  return name in table ? { mtimeMs: table[name] } : null;
};

test("a missing file of the scene is reported", () => {
  const r = missingOrStale(manifest, "sports", "d", SINCE, statFrom({ a: 0, "a.png": 2000 }));
  assert.deepEqual(r, { missing: ["b.png"], stale: [], foreign: [] });
});

test("a file older than the marker is stale", () => {
  const r = missingOrStale(manifest, "sports", "d", SINCE, statFrom({ "a.png": 2000, "b.png": 500 }));
  assert.deepEqual(r, { missing: [], stale: ["b.png"], foreign: [] });
});

test("another scene's file newer than the marker is foreign", () => {
  const r = missingOrStale(manifest, "sports", "d", SINCE, statFrom({ "a.png": 2000, "b.png": 2000, "c.png": 3000 }));
  assert.deepEqual(r, { missing: [], stale: [], foreign: ["c.png"] });
});

test("another scene's old or absent file is not foreign", () => {
  const r = missingOrStale(manifest, "full", "d", SINCE, statFrom({ "a.png": 10, "c.png": 2000 }));
  assert.deepEqual(r, { missing: [], stale: [], foreign: [] });
});

test("all good gives three empty lists", () => {
  const r = missingOrStale(manifest, "sports", "d", SINCE, statFrom({ "a.png": 1000, "b.png": 1500 }));
  assert.deepEqual(r, { missing: [], stale: [], foreign: [] });
});

test("the default stat reads the real directory", () => {
  const dir = mkdtempSync(join(tmpdir(), "shots-"));
  writeFileSync(join(dir, "a.png"), "x");
  const r = missingOrStale(manifest, "sports", dir, 0);
  assert.deepEqual(r.missing, ["b.png"]);
  assert.deepEqual(r.stale, []);
});

function fakeRepo() {
  const root = mkdtempSync(join(tmpdir(), "repo-"));
  const src = join(root, "web", "src");
  mkdirSync(join(src, "pages"), { recursive: true });
  mkdirSync(join(src, "assets", "shots"), { recursive: true });
  return { root, src };
}

test("usersOf finds a name in an .astro file and ignores one that only appears in a .test.ts", () => {
  const { src } = fakeRepo();
  writeFileSync(join(src, "pages", "features.astro"), `<img src="x/01-dashboard.png" />`);
  writeFileSync(join(src, "pages", "other.astro"), `<img src="x/02-routing.png" />`);
  writeFileSync(join(src, "pages", "other.test.ts"), `expect("01-dashboard.png")`);
  writeFileSync(join(src, "assets", "shots", "01-dashboard.png"), "binary 01-dashboard.png");
  assert.deepEqual(usersOf("01-dashboard.png", src), ["web/src/pages/features.astro"]);
  assert.deepEqual(usersOf("nothing.png", src), []);
});

test("usersOf does not count a name that is only in a comment", () => {
  const { src } = fakeRepo();
  writeFileSync(join(src, "pages", "a.astro"), `<!-- 01-dashboard.png -->`);
  writeFileSync(join(src, "pages", "b.ts"), `// 01-dashboard.png\n/* 01-dashboard.png */`);
  assert.deepEqual(usersOf("01-dashboard.png", src), []);
});

test("usersOf returns sorted paths", () => {
  const { src } = fakeRepo();
  writeFileSync(join(src, "pages", "z.astro"), "s.png");
  writeFileSync(join(src, "pages", "a.astro"), "s.png");
  assert.deepEqual(usersOf("s.png", src), ["web/src/pages/a.astro", "web/src/pages/z.astro"]);
});

test("withoutComments keeps the // of a URL", () => {
  assert.equal(
    withoutComments("/* a.png */ x // b.png\n<!-- c.png -->\nlet u = 'https://x/d.png'"),
    " x \n\nlet u = 'https://x/d.png'",
  );
});

// ---- the CLI, as a child process against a temporary folder ----

const SCRIPT = fileURLToPath(new URL("./check-shots.mjs", import.meta.url));

function cliFixture() {
  const { root, src } = fakeRepo();
  const dir = join(root, "media");
  mkdirSync(dir);
  const manifestPath = join(root, "shots.json");
  writeFileSync(manifestPath, JSON.stringify({ shots: [{ file: "a.png", scene: "sports", usedBy: [], site: false }] }));
  const run = (...args) =>
    spawnSync(process.execPath, [SCRIPT, ...args], {
      encoding: "utf8",
      env: { ...process.env, SHOT_MANIFEST: manifestPath, SHOT_DIR: dir, SHOT_WEB_SRC: src },
    });
  return { root, src, dir, manifestPath, run };
}

test("the CLI exits 2 on an unknown scene instead of passing vacuously", () => {
  const { root, run } = cliFixture();
  writeFileSync(join(root, "marker"), "");
  const r = run("sprots", join(root, "marker"));
  assert.equal(r.status, 2);
  assert.match(r.stderr, /no shot in the manifest has scene "sprots" \(known: sports\)/);
});

test("the CLI exits 2 on a missing marker file, with a message and no stack", () => {
  const { root, run } = cliFixture();
  const r = run("sports", join(root, "no-such-marker"));
  assert.equal(r.status, 2);
  assert.match(r.stderr, /marker file .* does not exist/);
  assert.doesNotMatch(r.stderr, /\n\s+at /);
});

test("the CLI exits 2 on missing arguments", () => {
  assert.equal(cliFixture().run().status, 2);
});

test("the CLI exits 1 and lists what is missing, then 0 when it is there", () => {
  const { root, dir, run } = cliFixture();
  const marker = join(root, "marker");
  writeFileSync(marker, "");
  const bad = run("sports", marker);
  assert.equal(bad.status, 1);
  assert.match(bad.stdout, /missing: a\.png/);
  writeFileSync(join(dir, "a.png"), "x");
  const later = new Date(Date.now() + 5000);
  utimesSync(join(dir, "a.png"), later, later);
  assert.equal(run("sports", marker).status, 0);
});

test("the CLI --write-used-by writes usedBy and site from the code", () => {
  const { src, manifestPath, run } = cliFixture();
  writeFileSync(join(src, "pages", "p.astro"), `<img src="a.png">`);
  assert.equal(run("--write-used-by").status, 0);
  const s = JSON.parse(readFileSync(manifestPath, "utf8")).shots[0];
  assert.deepEqual(s.usedBy, ["web/src/pages/p.astro"]);
  assert.equal(s.site, true);
});
