#!/usr/bin/env node
// The screenshot manifest's two derivations and its post-capture check.
//
//   node scripts/check-shots.mjs <scene> <marker-file>   after a capture run:
//        lists the scene's shots that are missing or older than the marker
//        file, and other scenes' shots that were touched since (a run that
//        wrote what it should not have). Exits 1 if any list is non-empty.
//   node scripts/check-shots.mjs --write-used-by         rewrites `usedBy` and
//        `site` in docs/media/shots.json from the code. They are never typed.
//
// Imported by web/src/lib/media-manifest.test.ts and shots-usage.test.ts, so
// the test and the generator cannot disagree about who uses a shot.

import { readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));

/** Site copies kept with no page using them, on purpose. Plan 4 removes an
 *  entry when it uses the file. web/src/lib/shots-usage.test.ts pins the length. */
export const KEPT_WITHOUT_USER = ["06-renditions.png"];

/** A source with its comments removed: a file name in a comment is not a use. */
export function withoutComments(text) {
  return text
    .replace(/\/\*[\s\S]*?\*\//g, "") // block comments (CSS, JS)
    .replace(/<!--[\s\S]*?-->/g, "") // HTML and markdown comments
    .replace(/(^|[^:"'`\\])\/\/[^\n]*/g, "$1"); // line comments, not the // of a URL
}

/** Every source file under `dir` a page could use an image from: not tests, not
 *  binaries, not `skipDir` (the folder the shots themselves are in).
 *  @param {string} dir
 *  @param {string} skipDir
 *  @returns {string[]} */
export function sourceFiles(dir, skipDir) {
  /** @type {string[]} */
  const out = [];
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (p === skipDir) continue;
    if (e.isDirectory()) out.push(...sourceFiles(p, skipDir));
    else if (!/\.test\.[a-z]+$/.test(e.name) && !/\.(png|jpe?g|webp|avif|woff2?|glb)$/.test(e.name)) out.push(p);
  }
  return out;
}

/** The sorted repo-relative paths of the files under web/src whose text, comments
 *  removed, contains `file`. Test files and web/src/assets/shots/ do not count. */
export function usersOf(file, webSrcRoot) {
  const root = resolve(webSrcRoot);
  const repo = resolve(root, "..", "..");
  return sourceFiles(root, join(root, "assets", "shots"))
    .filter((p) => withoutComments(readFileSync(p, "utf8")).includes(file))
    .map((p) => relative(repo, p).split("\\").join("/"))
    .sort((a, b) => a.localeCompare(b));
}

function realStat(p) {
  try {
    return statSync(p);
  } catch {
    return null;
  }
}

/** Files of `scene` absent from `dir`, files of it older than `sinceMs`, and
 *  files of OTHER scenes touched since. `statFn(path)` returns {mtimeMs} or null. */
export function missingOrStale(manifest, scene, dir, sinceMs, statFn = realStat) {
  const missing = [];
  const stale = [];
  const foreign = [];
  for (const s of manifest.shots) {
    const st = statFn(join(dir, s.file));
    if (s.scene === scene) {
      if (!st) missing.push(s.file);
      else if (st.mtimeMs < sinceMs) stale.push(s.file);
    } else if (st && st.mtimeMs >= sinceMs) {
      foreign.push(s.file);
    }
  }
  return { missing, stale, foreign };
}

/** The CLI. Exit 0 all good, 1 something to report, 2 a usage or input error.
 *  SHOT_MANIFEST, SHOT_WEB_SRC and SHOT_DIR move its three inputs (the tests
 *  point them at a temporary folder). */
export function main(argv, env = process.env) {
  const manifestPath = env.SHOT_MANIFEST ?? join(HERE, "..", "docs", "media", "shots.json");
  const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
  if (argv[0] === "--write-used-by") {
    const webSrc = env.SHOT_WEB_SRC ?? join(HERE, "..", "web", "src");
    for (const s of manifest.shots) {
      s.usedBy = usersOf(s.file, webSrc);
      s.site = s.usedBy.length > 0 || KEPT_WITHOUT_USER.includes(s.file);
    }
    writeFileSync(manifestPath, JSON.stringify(manifest, null, 2) + "\n");
    return 0;
  }
  const [scene, marker] = argv;
  if (!scene || !marker) {
    console.error("usage: check-shots.mjs <scene> <marker-file> | --write-used-by");
    return 2;
  }
  if (!manifest.shots.some((s) => s.scene === scene)) {
    const known = [...new Set(manifest.shots.map((s) => s.scene))].join(", ");
    console.error(`no shot in the manifest has scene "${scene}" (known: ${known})`);
    return 2;
  }
  const markerStat = realStat(marker);
  if (!markerStat) {
    console.error(`marker file ${marker} does not exist`);
    return 2;
  }
  const dir = env.SHOT_DIR ?? join(HERE, "..", "docs", "media");
  const r = missingOrStale(manifest, scene, dir, markerStat.mtimeMs);
  for (const [label, list] of Object.entries(r)) {
    if (list.length > 0) console.log(`${label}: ${list.join(", ")}`);
  }
  return Object.values(r).some((l) => l.length > 0) ? 1 : 0;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = main(process.argv.slice(2));
}
