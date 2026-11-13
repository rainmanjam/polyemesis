// node --test scripts/check-shots.test.mjs
import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

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
