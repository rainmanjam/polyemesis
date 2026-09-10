/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { join } from "node:path";

/* THE DESIGN SYSTEM IS ONE SYSTEM, AND THIS IS WHAT SAYS SO.
 *
 * docs/DESIGN-SYSTEM.md has claimed since 2026-07-29 that "the app's tokens
 * ship to the website unchanged" and that the website "includes the same
 * block". Nothing ever checked either sentence, and both were false by the time
 * anyone measured:
 *
 *   - 13 of the app's token names appeared in the website's stylesheet at all;
 *   - --surface, --muted and --down were DIFFERENT COLOURS in the two products;
 *   - the website @font-face-loaded IBM Plex Sans and JetBrains Mono while the
 *     app named no webfont at all and fell through to system-ui, so the two
 *     halves of one product rendered in different typefaces — which is the
 *     single most visible reason they did not look like one product.
 *
 * A comment in ui/src/index.css asserted the palettes were "identical" through
 * all of that. This file is the difference between asserting it and enforcing
 * it: the shared block is copied into web/ by scripts/sync-design-tokens.mjs,
 * and every commit that separates the copy from the original fails here.
 *
 * WHY THE TEST LIVES IN ui/ AND READS web/. ci.yml runs the ui job — and
 * therefore `npm test` — on every pull request whose changes are not purely
 * documentation, in both directions: editing web/src/styles/tokens.css by hand
 * turns this red just as editing ui/src/index.css does. pages.yml, which is the
 * only workflow that builds web/, runs on `web/**` alone and would never see a
 * token changed on the app's side. The guard has to sit on the side that is
 * always watched.
 *
 * WHAT IT CANNOT DO, said out loud so a green run is not read as a stronger
 * claim: this compares stylesheets, not renderings. It cannot tell you that a
 * component uses the right token, that a colour is legible, or that the
 * website's Tailwind adapter maps a token to the role it was designed for. It
 * says the two files hold one palette, one type scale, one motion vocabulary
 * and one pair of typefaces — the thing that was silently untrue.
 */

const SRC = new URL("../", import.meta.url).pathname; // ui/src/
const ROOT = join(SRC, "..", ".."); // repository root
const APP_CSS = join(SRC, "index.css");
const SITE_TOKENS = join(ROOT, "web", "src", "styles", "tokens.css");
const SITE_CSS = join(ROOT, "web", "src", "styles", "global.css");

/* A floor, not a number with meaning. The block held 58 tokens when this was
 * written; the point of the assertion is that a parser which has silently
 * stopped matching — a reformat, a rename, a regex that no longer fits — cannot
 * report "the two sides agree" about nothing at all. 40 is low enough that
 * deleting a token group does not fail the wrong test and high enough that a
 * broken walk cannot pass. */
const MIN_TOKENS = 40;

/** The `:root { … }` block, by its own braces.
 *
 *  Anchored to a line that is exactly `:root {` and closed by a `}` at the
 *  start of a line, which is what scripts/sync-design-tokens.mjs relies on too.
 *  Both throw rather than return a short answer when that shape changes: a
 *  half-read block would compare two truncated palettes and pass. */
function rootBlock(css: string, where: string): string {
  const start = css.search(/^:root\s*\{$/m);
  if (start < 0) throw new Error(`${where} has no ':root {' at the start of a line`);
  const end = css.indexOf("\n}\n", start);
  if (end < 0) throw new Error(`${where} has a ':root {' that is never closed`);
  return css.slice(start, end + 3);
}

/** Comments removed, so a token quoted in prose is never mistaken for one that
 *  is declared. The block is thick with explanation and several comments name
 *  other tokens; without this, `--color-cross` discussed in a note would parse
 *  as a declaration. */
function stripComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, "");
}

/** name → value for every custom property declared in a `:root` block.
 *
 *  THROWS ON AN EMPTY RESULT, and that is the positive control this whole file
 *  turns on. Two stylesheets that yield no tokens compare equal, and an equality
 *  assertion over two empty maps is the most confident-looking way there is to
 *  check nothing. The failure that would produce it is not hypothetical: the
 *  block moving, being renamed, or being reformatted by a tool would each leave
 *  the regex matching zero declarations while every test below still passed.
 *  See "a walk that matches nothing is not agreement" in the tests. */
function tokensIn(css: string, where: string): Map<string, string> {
  const out = new Map<string, string>();
  for (const m of stripComments(rootBlock(css, where)).matchAll(/(--[a-z0-9-]+)\s*:\s*([^;]+);/g)) {
    out.set(m[1], m[2].trim().replace(/\s+/g, " "));
  }
  if (out.size < MIN_TOKENS) {
    throw new Error(
      `${where}: found ${out.size} tokens, expected at least ${MIN_TOKENS}. ` +
        "Either the design system has been gutted or this parser has stopped " +
        "matching it. Both are failures; agreement about an empty set is not.",
    );
  }
  return out;
}

const appCss = readFileSync(APP_CSS, "utf8");
const siteTokensCss = readFileSync(SITE_TOKENS, "utf8");
const siteCss = readFileSync(SITE_CSS, "utf8");

const REGENERATE = "run `node scripts/sync-design-tokens.mjs` from the repository root";

describe("the app and the website hold one set of design tokens", () => {
  it("finds a design system on both sides at all", () => {
    // Guards the guard. Every comparison below is vacuous if either walk
    // returns nothing, and returning nothing is exactly what a moved or
    // reformatted block would do.
    expect(tokensIn(appCss, "ui/src/index.css").size).toBeGreaterThanOrEqual(MIN_TOKENS);
    expect(tokensIn(siteTokensCss, "web/src/styles/tokens.css").size).toBeGreaterThanOrEqual(MIN_TOKENS);
  });

  it("a walk that matches nothing is not agreement", () => {
    /* THE POSITIVE CONTROL. Two stylesheets with no tokens in them agree
     * perfectly and mean nothing, so the parser refuses to hand back an empty
     * comparison rather than letting one pass as a green check. This is the
     * failure mode that made the original claim survive: something that
     * examined nothing and reported no difference. */
    const empty = ":root {\n  color-scheme: dark;\n}\n";
    expect(() => tokensIn(empty, "a stylesheet with no tokens")).toThrow(/expected at least/);
    // And a block that has lost most of itself must fail too — a truncated
    // parse is the realistic version of this, not a literally empty file.
    const partial = ":root {\n  --background: red;\n  --primary: blue;\n}\n";
    expect(() => tokensIn(partial, "a truncated block")).toThrow(/found 2 tokens/);
  });

  it("declares the same token names on both sides", () => {
    const app = [...tokensIn(appCss, "ui/src/index.css").keys()].sort();
    const site = [...tokensIn(siteTokensCss, "web/src/styles/tokens.css").keys()].sort();
    expect(
      site,
      `the website is missing tokens the app declares, or carries ones it does ` +
        `not. The website's copy is generated: ${REGENERATE}.`,
    ).toEqual(app);
  });

  it("gives every token the same value on both sides", () => {
    const app = tokensIn(appCss, "ui/src/index.css");
    const site = tokensIn(siteTokensCss, "web/src/styles/tokens.css");
    const differ: string[] = [];
    for (const [name, value] of app) {
      const theirs = site.get(name);
      if (theirs !== value) differ.push(`${name}: app ${value} / site ${theirs ?? "(absent)"}`);
    }
    expect(
      differ,
      `these tokens name one thing and are two colours. This is the defect the ` +
        `design system was written to prevent -- --surface, --muted and --down ` +
        `had already drifted this way once. ${REGENERATE}.`,
    ).toEqual([]);
  });

  it("keeps the website's copy verbatim, comments included", () => {
    /* Stronger than the two assertions above, and deliberately so. The
     * reasoning attached to a token is the most useful thing about it, and a
     * reader who meets the palette in a website stylesheet has nowhere else to
     * find out why --warn must not be folded into --color-cross. A copy that
     * keeps the values and drops the arguments is how the next person makes the
     * same mistake with the same confidence. */
    const block = rootBlock(appCss, "ui/src/index.css");
    const at = siteTokensCss.indexOf(block);
    expect(
      at,
      `web/src/styles/tokens.css is not a verbatim copy of the :root block in ` +
        `ui/src/index.css. It is a generated file: ${REGENERATE}.`,
    ).toBeGreaterThanOrEqual(0);

    /* CONTAINMENT IS NOT COPYING.
     *
     * `includes` was the whole check, and `includes` is blind to anything the
     * file holds IN ADDITION. Append a second `:root { --color-down: hotpink }`
     * after the generated block and every assertion in this file still passes:
     * the copy is verbatim, it is present, the site imports it -- and the later
     * block wins the cascade, so the website renders a palette the app has
     * never heard of while the guard reports one design system.
     *
     * It is also the likeliest way for this file to be edited. The header says
     * DO NOT EDIT, which is rung 0, and the natural way to disregard it is to
     * leave the generated part alone and add underneath. */
    expect(
      siteTokensCss.slice(at + block.length).trim(),
      `web/src/styles/tokens.css has content AFTER the generated :root block. ` +
        `Whatever it declares wins the cascade over the block above it, so the ` +
        `website stops rendering the app's palette while every other assertion ` +
        `here still passes. Put the change in ui/src/index.css: ${REGENERATE}.`,
    ).toBe("");

    expect(
      [...siteTokensCss.matchAll(/^:root\s*\{$/gm)].length,
      `web/src/styles/tokens.css declares more than one :root block. Only the ` +
        `generated one is checked against the app; the others are unguarded.`,
    ).toBe(1);
  });

  it("has the website actually including the block, not merely holding it", () => {
    // A shared stylesheet nobody imports is a file, not a design system. This
    // is the cheapest way for the whole mechanism to be silently switched off.
    expect(
      siteCss,
      "web/src/styles/global.css must @import ./tokens.css, or the website " +
        "renders from Tailwind's defaults while every test above passes.",
    ).toMatch(/@import\s+["']\.\/tokens\.css["']/);
  });
});

/* ----------------------------------------------- no second copy of a value */

/** Tokens the website is allowed to hold as its own literal even though the
 *  same value exists in the shared block.
 *
 *  ONE ENTRY, AND IT IS A DECISION RATHER THAN AN EXCEPTION. --color-cross is
 *  bit-for-bit the app's --warn and must stay a duplicate: the app's token
 *  means "reconnecting / degraded / near clip" to an operator watching a live
 *  broadcast, and the website's means "this crosspoint is lit". Wiring them
 *  together would let a designer retuning a marketing page move the colour an
 *  operator reads a failing stream by. Both stylesheets carry the long form of
 *  this note beside the value. */
const DUPLICATE_ON_PURPOSE = new Set(["--color-cross", "--color-cross-dim"]);

describe("the website reads shared values rather than restating them", () => {
  it("holds no second copy of an app literal", () => {
    const shared = new Map(
      [...tokensIn(appCss, "ui/src/index.css")].map(([name, value]) => [value, name]),
    );
    /* THE ANCHOR IS CHECKED BEFORE IT IS USED.
     *
     * `indexOf` returns -1 when the anchor is gone, and `slice(-1)` is the LAST
     * CHARACTER of the file -- so the loop below walks one byte, matches
     * nothing, and reports no restated literals. The guard does not fail when
     * it loses its footing; it passes, over nothing.
     *
     * The move that does it is ordinary and correct: Tailwind v4's own docs use
     * `@theme inline {`, and renaming the block that way is a legitimate edit
     * that silently switches this check off for good. */
    const themeAt = siteCss.indexOf("@theme {");
    expect(
      themeAt,
      "web/src/styles/global.css has no `@theme {` block. This test slices from " +
        "that anchor, and a missing one makes it scan a single character and " +
        "pass over nothing -- so the anchor is asserted rather than assumed. If " +
        "the block was renamed (`@theme inline {` is the Tailwind v4 spelling), " +
        "update the anchor here in the same commit.",
    ).toBeGreaterThanOrEqual(0);
    const theme = siteCss.slice(themeAt);
    const restated: string[] = [];
    for (const m of stripComments(theme).matchAll(/(--[a-z0-9-]+)\s*:\s*([^;]+);/g)) {
      const name = m[1];
      const value = m[2].trim().replace(/\s+/g, " ");
      if (DUPLICATE_ON_PURPOSE.has(name)) continue;
      const twin = shared.get(value);
      if (twin) restated.push(`${name}: ${value} is the app's ${twin}, written out again`);
    }
    expect(
      restated,
      "a literal here that equals a shared token is the drift this whole " +
        "mechanism exists to stop: the two copies are correct on the day they " +
        "are written and independent forever after. Read the token instead -- " +
        "`var(--the-app-name)` -- or, if the duplication is deliberate, add it " +
        "to DUPLICATE_ON_PURPOSE with the reason it must not be shared.",
    ).toEqual([]);
  });
});

/* ------------------------------------------------------------- the typefaces */

describe("the app and the website render in the same two typefaces", () => {
  /** The families the shared block names first, which is what actually gets
   *  used, paired with the file each is served from. */
  const FACES = [
    { token: "--font-sans", family: "IBM Plex Sans", file: "ibm-plex-sans.woff2" },
    { token: "--font-mono", family: "JetBrains Mono", file: "jetbrains-mono.woff2" },
  ];

  it("names the same family first in both stacks", () => {
    const app = tokensIn(appCss, "ui/src/index.css");
    for (const { token, family } of FACES) {
      const stack = app.get(token) ?? "";
      expect(stack, `${token} must lead with "${family}"`).toMatch(
        new RegExp(`^"${family}"`),
      );
    }
  });

  it("self-hosts every family it names, rather than declaring one it never loads", () => {
    /* THE ORIGINAL DEFECT, TURNED INTO AN ASSERTION. The app declared
     * --font-sans for months and loaded no font file, so the token was true and
     * the rendering was not — a stack whose first family is absent falls
     * silently through to the next one, which is why nobody could see it.
     * Naming a family without an @font-face for it fails here now. */
    for (const { family, file } of FACES) {
      const face = new RegExp(
        `@font-face\\s*\\{[^}]*font-family:\\s*"${family}"[^}]*url\\("/fonts/${file}"\\)`,
      );
      expect(
        appCss,
        `ui/src/index.css names "${family}" but never @font-face-loads ` +
          `/fonts/${file}; the app would render in the next family in the stack ` +
          `and look like a different product from the website.`,
      ).toMatch(face);
    }
  });

  it("serves the same font files the website serves", () => {
    /* Byte-identical, not "the same family". Two subsets of one family differ
     * in coverage, hinting and metrics — all of which are invisible in review
     * and obvious when the app and the site are open side by side. The files
     * are duplicated because the two projects have separate public/ directories
     * and no shared build step; this is what makes the duplication safe. */
    const sha = (p: string) => createHash("sha256").update(readFileSync(p)).digest("hex");
    for (const { file } of FACES) {
      expect(
        sha(join(ROOT, "ui", "public", "fonts", file)),
        `ui/public/fonts/${file} differs from web/public/fonts/${file}. Copy ` +
          `the website's file over the app's rather than re-subsetting one side.`,
      ).toBe(sha(join(ROOT, "web", "public", "fonts", file)));
    }
  });
});
