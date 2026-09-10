#!/usr/bin/env node
/* Copy the design system's :root block from the app into the website.
 *
 * WHAT THIS IS FOR. docs/DESIGN-SYSTEM.md says the system's source of truth is
 * a plain CSS custom-property block — the `:root` section of ui/src/index.css —
 * and that "the website includes the same block". Between the day that was
 * written and the day it was measured, nothing did the including: 13 of the
 * app's 110 token names appeared on the site at all, and three that did
 * (--surface, --muted, --down) were different colours in the two products. This
 * script is the including.
 *
 * WHY A CHECKED-IN COPY rather than an import, a package or a build step. All
 * three were considered and each one breaks somewhere real:
 *
 *   - A relative @import ("../../../ui/src/styles/...") is the cheapest thing
 *     that could work and is the one that cannot. web/Dockerfile builds the
 *     site from the web/ directory as its context — `COPY . .` with a build
 *     context of web/ — so a path reaching up into ui/ is a file that does not
 *     exist in the image, and the failure appears only when someone builds the
 *     container rather than when they build locally.
 *   - A shared npm package means a registry, a version and a release process
 *     for two consumers in one repository. DESIGN-SYSTEM.md rejects that by
 *     name under "What is deliberately absent".
 *   - Generating web/src/styles/tokens.css during the Astro build has the same
 *     problem as the import: the generator would have to read ui/, which is not
 *     in the image either.
 *
 * A file checked into web/ is the only one of the four that survives both build
 * systems, because after this script has run the website needs nothing outside
 * web/ at all. The cost of a copy is that it can go stale, and that cost is
 * paid by ui/src/lib/design-tokens.test.ts, which compares the two blocks token
 * by token on every CI run and fails the moment they differ. Copying is safe
 * exactly when something checks the copy.
 *
 * Usage:  node scripts/sync-design-tokens.mjs          (writes)
 *         node scripts/sync-design-tokens.mjs --check  (exits 1 if stale)
 */

import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");
const SOURCE = join(ROOT, "ui", "src", "index.css");
const TARGET = join(ROOT, "web", "src", "styles", "tokens.css");

/* The `:root { … }` block, taken by its own braces.
 *
 * Anchored to a `:root {` that starts a line and to a `}` that starts a line,
 * which is what the file's formatting guarantees and Prettier keeps true. A
 * brace-counting parser would be more general and would also have to understand
 * strings and comments to be correct; this reads one block out of one file
 * whose shape is fixed, and it fails loudly rather than returning something
 * shorter than it should when that shape changes.
 */
export function extractRootBlock(css) {
  const start = css.search(/^:root\s*\{$/m);
  if (start < 0) {
    throw new Error(
      "no `:root {` at the start of a line in ui/src/index.css. The design " +
        "system's source of truth is that block; if it has been renamed or " +
        "reformatted, this script and design-tokens.test.ts both need to know.",
    );
  }
  const end = css.indexOf("\n}\n", start);
  if (end < 0) throw new Error("the :root block in ui/src/index.css is not closed by a `}` on its own line");
  return css.slice(start, end + 3);
}

const HEADER = `/* GENERATED FILE — DO NOT EDIT.
 *
 * The design system's :root block, copied verbatim from ui/src/index.css by
 * scripts/sync-design-tokens.mjs. The app is where a token is decided; this is
 * the website receiving it, so that one palette, one type scale, one motion
 * vocabulary and one pair of typefaces serve both halves of the product.
 *
 * Editing this file is how the two products stop looking like one. Change
 * ui/src/index.css and re-run the script; ui/src/lib/design-tokens.test.ts
 * fails on any commit where these two blocks disagree, including one that edits
 * only this side.
 *
 * The comments below are the app's, kept rather than stripped: the reasoning
 * for a colour is the most useful thing about it, and a reader who arrives at
 * this file from a website stylesheet has nowhere else to find it.
 */

`;

const block = extractRootBlock(readFileSync(SOURCE, "utf8"));
const out = HEADER + block;

if (process.argv.includes("--check")) {
  const current = readFileSync(TARGET, "utf8");
  if (current !== out) {
    console.error(
      "web/src/styles/tokens.css is stale. Run: node scripts/sync-design-tokens.mjs",
    );
    process.exit(1);
  }
  console.log("web/src/styles/tokens.css is in sync with ui/src/index.css");
} else {
  writeFileSync(TARGET, out);
  const count = [...block.matchAll(/^\s{2}--[a-z0-9-]+:/gm)].length;
  console.log(`wrote ${TARGET} — ${count} tokens`);
}
