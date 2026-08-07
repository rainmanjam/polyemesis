#!/usr/bin/env node
/* Assertions about the BUILT output, not the source.
 *
 * These exist because the source can be correct while the shipped CSS is wrong.
 * The scroll-driven reveals were written as
 *
 *     animation: rise 0.6s linear both;
 *     animation-timeline: view();
 *
 * which is valid, and works in dev. Lightning CSS merged the two into the
 * shorthand `animation: .6s linear both rise view()`, which Chrome rejects
 * outright — computed animation-name came back `none`, so every reveal on the
 * site silently did nothing. No amount of reading the source would have caught
 * it, and the page looked fine, because the failure mode was "content is
 * simply always visible".
 */
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";

const DIST = new URL("../dist/", import.meta.url).pathname;
const fail = [];

const cssFiles = readdirSync(join(DIST, "_astro")).filter((f) => f.endsWith(".css"));
const css = cssFiles.map((f) => readFileSync(join(DIST, "_astro", f), "utf8")).join("\n");

// 1. animation-timeline must survive as its own declaration.
const folded = /animation:[^;}]*\bview\(\)/.exec(css);
if (folded) {
  fail.push(
    `animation-timeline was folded into an \`animation\` shorthand, which Chrome drops:\n` +
    `      ${folded[0].slice(0, 90)}\n` +
    `    Use the longhands so the minifier has no shorthand to merge into.`,
  );
}
if (!/animation-timeline:\s*view\(\)/.test(css)) {
  fail.push("no `animation-timeline: view()` in the built CSS — scroll reveals are not shipping");
}

// 2. Reduced motion must switch scroll-driven reveals OFF, not shorten them.
if (!/@media\s*\(prefers-reduced-motion/.test(css)) {
  fail.push("no prefers-reduced-motion block in the built CSS");
} else if (!/animation-name:\s*none\s*!important/.test(css)) {
  fail.push("reduced motion does not set `animation-name: none !important` — a scroll-driven reveal ignores animation-duration");
}

// 3+4. Internal links and fragments must resolve to something that was built.
const pages = readdirSync(DIST).filter((f) => f.endsWith(".html"));
const built = new Set(pages.map((f) => "/" + f.replace(/\.html$/, "").replace(/^index$/, "")));
for (const f of pages) {
  const html = readFileSync(join(DIST, f), "utf8");
  for (const m of html.matchAll(/href="(\/[^"#?]*)(#[^"]*)?"/g)) {
    const path = m[1].replace(/\/$/, "") || "/";
    if (path.startsWith("/_astro") || /\.[a-z0-9]+$/i.test(path)) continue;
    if (!built.has(path)) fail.push(`${f}: links to ${m[1]}, which was not built`);
  }
  for (const m of html.matchAll(/href="(\/[a-z]*)#([a-z0-9-]+)"/g)) {
    const target = m[1] === "/" ? "index.html" : `${m[1].slice(1)}.html`;
    if (!pages.includes(target)) continue;
    if (!new RegExp(`id="${m[2]}"`).test(readFileSync(join(DIST, target), "utf8"))) {
      fail.push(`${f}: links to ${m[1]}#${m[2]} but ${target} has no element with that id`);
    }
  }
}

// 5. The amber reservation, enforced rather than merely documented.
//
//    Amber (--color-cross / #e8a33d) marks the signal path: a lit crosspoint,
//    the route it feeds, and that route running hot. The rule drifted once
//    already — it said "crosspoint and nothing else" while the hero's routes
//    had always been amber, and a comparison-page callout had quietly picked it
//    up as a general accent. Prose in a stylesheet did not stop that; this does.
const amberAllowed = {
  // The hero's route lines and their junction dot.
  "index.html": 3,
};
for (const f of pages) {
  const html = readFileSync(join(DIST, f), "utf8");
  const hits = (html.match(/e8a33d/gi) || []).length;
  const allowed = amberAllowed[f] ?? 0;
  if (hits !== allowed) {
    fail.push(
      `${f}: ${hits} literal amber (#e8a33d) use(s), expected ${allowed}. ` +
      `Amber is reserved for the signal path — see the rule at the top of global.css. ` +
      `If this is deliberate, update amberAllowed here and say why there.`,
    );
  }
  // Utility-class amber on markup is always an accent use; the crosspoint gets
  // its amber from the .xpt rules and the level readout from the matrix script.
  for (const cls of ["border-cross", "bg-cross", "text-cross"]) {
    if (new RegExp(`class="[^"]*\\b${cls}\\b`).test(html)) {
      fail.push(`${f}: \`${cls}\` on markup — amber is not a general accent. Use primary.`);
    }
  }
}

if (fail.length) {
  console.error("build checks FAILED:\n" + fail.map((f) => "  - " + f).join("\n"));
  process.exit(1);
}
console.log(`build checks passed (${pages.length} pages, ${cssFiles.length} stylesheet)`);
