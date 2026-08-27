#!/usr/bin/env node
/* Publish docs/ to the GitHub wiki.
 *
 *   node scripts/publish-wiki.mjs            # write to a staging dir, change nothing
 *   node scripts/publish-wiki.mjs --push     # clone the wiki, write, commit, push
 *
 * WHAT IT PUBLISHES, and why that question is already answered. Every file in
 * docs/ is classified in web/src/data/docs.mjs as PUBLISHED or NOT_PUBLISHED,
 * and the site build REFUSES a file in neither. That gate exists so publishing
 * a doc is a decision somebody wrote down rather than the default, and this
 * script inherits it by importing the same module: the wiki cannot disagree
 * with the website about what is public, because neither has its own list.
 *
 * NOT_PUBLISHED stays out. That set is design notes for unshipped work and
 * internal-facing files, and a wiki page for a design note reads as a roadmap
 * commitment to anyone who finds it.
 *
 * WHY EVERY PAGE CARRIES A WARNING. A wiki is a separate git repository with no
 * CI and no review, so a page edited there is a fork of the documentation that
 * nothing will ever reconcile — and the reader cannot tell. The header names
 * the source file and says where to edit instead. It is rung 2: it cannot stop
 * an edit, it announces that one is about to be lost.
 *
 * Re-runnable by design. Running it again overwrites every generated page, so
 * drift is one command away from fixed rather than a merge.
 */
import { readFileSync, writeFileSync, mkdirSync, rmSync, existsSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { execFileSync } from "node:child_process";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");
const STAGE = join(ROOT, ".work", "wiki");
const WIKI = "https://github.com/rainmanjam/polyemesis.wiki.git";

const { PUBLISHED, SECTIONS } = await import(join(ROOT, "web/src/data/docs.mjs"));

/** The wiki flattens: docs/QUICKSTART.md and docs/internal/X.md would collide
 *  on a bare basename, so the slug carries the path. */
const pageName = (file) => file.replace(/\.md$/, "").replace(/\//g, "-");

function header(file) {
  return [
    `> **Generated from [\`docs/${file}\`](https://github.com/rainmanjam/polyemesis/blob/main/docs/${file}).**`,
    `> Edit that file and re-run \`node scripts/publish-wiki.mjs --push\`.`,
    `> An edit made here is a fork of the documentation that nothing reconciles,`,
    `> and the next run of this script will overwrite it without asking.`,
    "",
    "",
  ].join("\n");
}

/** Rewrite in-repo links so they resolve on the wiki.
 *
 *  A relative `[x](INSTALL.md)` is a 404 here: the wiki is flat and has no
 *  docs/ directory. Published targets become wiki pages; unpublished ones
 *  become absolute links back to the repository, which is the honest answer —
 *  the page exists, it is simply not part of this wiki. */
function rewriteLinks(body, published) {
  return body.replace(/\]\(([^)]+\.md)(#[^)]*)?\)/g, (whole, target, frag = "") => {
    const clean = target.replace(/^\.\//, "");
    if (published.has(clean)) return `](${pageName(clean)}${frag})`;
    if (clean.startsWith("../") || clean.includes("/")) {
      return `](https://github.com/rainmanjam/polyemesis/blob/main/docs/${clean.replace(/^\.\.\//, "")}${frag})`;
    }
    return `](https://github.com/rainmanjam/polyemesis/blob/main/docs/${clean}${frag})`;
  });
}

const publishedFiles = new Set(PUBLISHED.map((d) => d.file));

rmSync(STAGE, { recursive: true, force: true });
mkdirSync(STAGE, { recursive: true });

let written = 0;
for (const doc of PUBLISHED) {
  const src = join(ROOT, "docs", doc.file);
  if (!existsSync(src)) {
    console.error(`  MISSING ${doc.file} — listed as published and not on disk`);
    process.exitCode = 1;
    continue;
  }
  const body = rewriteLinks(readFileSync(src, "utf8"), publishedFiles);
  writeFileSync(join(STAGE, `${pageName(doc.file)}.md`), header(doc.file) + body);
  written++;
}

// Home and the sidebar are grouped by the same sections the website uses, so
// the two never disagree about where a document belongs.
const bySection = new Map();
for (const d of PUBLISHED) {
  if (!bySection.has(d.section)) bySection.set(d.section, []);
  bySection.get(d.section).push(d);
}
const home = [
  "# polyemesis",
  "",
  "> **Generated from `docs/` — do not edit here.**",
  "> Run `node scripts/publish-wiki.mjs --push` to regenerate.",
  "",
  "Self-hosted live multistreaming: ingest once, fan out to YouTube, Twitch,",
  "Facebook and Kick with a different audio mix per destination.",
  "",
];
const sidebar = ["### polyemesis", ""];
for (const s of SECTIONS) {
  const docs = bySection.get(s.id);
  if (!docs?.length) continue;
  home.push(`## ${s.title}`, "");
  if (s.blurb) home.push(s.blurb, "");
  sidebar.push(`**${s.title}**`, "");
  for (const d of docs) {
    home.push(`- **[${d.title}](${pageName(d.file)})** — ${d.description}`);
    sidebar.push(`- [${d.title}](${pageName(d.file)})`);
  }
  home.push("");
  sidebar.push("");
}
writeFileSync(join(STAGE, "Home.md"), home.join("\n"));
writeFileSync(join(STAGE, "_Sidebar.md"), sidebar.join("\n"));

console.log(`  ${written} page(s) + Home + _Sidebar staged in .work/wiki`);

if (!process.argv.includes("--push")) {
  console.log("  dry run — pass --push to publish");
  process.exit(process.exitCode ?? 0);
}

const clone = join(ROOT, ".work", "wiki-repo");
rmSync(clone, { recursive: true, force: true });
try {
  execFileSync("git", ["clone", "--depth", "1", WIKI, clone], { stdio: "pipe" });
} catch {
  console.error(
    "\n  The wiki repository does not exist yet.\n" +
      "  GitHub creates it only when the FIRST page is saved, and there is no API for that.\n" +
      "  Open https://github.com/rainmanjam/polyemesis/wiki, click 'Create the first page',\n" +
      "  save anything at all, then run this again — it will overwrite whatever you saved.",
  );
  process.exit(1);
}
for (const f of ["Home.md", "_Sidebar.md", ...PUBLISHED.map((d) => `${pageName(d.file)}.md`)]) {
  writeFileSync(join(clone, f), readFileSync(join(STAGE, f), "utf8"));
}
execFileSync("git", ["-C", clone, "add", "-A"], { stdio: "inherit" });
const staged = execFileSync("git", ["-C", clone, "status", "--porcelain"], { encoding: "utf8" });
if (!staged.trim()) {
  console.log("  wiki already matches docs/ — nothing to push");
  process.exit(0);
}
execFileSync("git", ["-C", clone, "commit", "-m", "docs: regenerate wiki from docs/"], { stdio: "inherit" });
execFileSync("git", ["-C", clone, "push"], { stdio: "inherit" });
console.log("  pushed");
