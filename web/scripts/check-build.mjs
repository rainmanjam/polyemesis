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

/* The two mobile-overflow fixes must still be IN the shipped CSS.
 *
 * These guard a fix, not a behaviour, and the distinction is worth stating: a
 * static file cannot prove no page overflows on a phone -- that needs a browser
 * and a real layout. What it can prove is that the two declarations that made
 * the overflow go away are still present, and deleting one of them is the
 * realistic way this regresses, because both look inert when you read them.
 *
 * Measured before the fix, at a 375px viewport: /download scrolled to 630px and
 * /comparison to 570px. Both were the whole document moving sideways, not an
 * element scrolling inside its own box.
 */
// `min-width: 0` lets a grid child shrink past its content's intrinsic minimum.
// Without it a card holding a long <pre> pushes the document wider than the
// screen, and the <pre>'s own overflow-x never engages.
if (!/\.card\{[^}]*min-width:0/.test(css.replace(/\s+/g, ""))) {
  fail.push(
    "`.card` has no `min-width: 0` — a card containing a <pre> will push the whole page wider than a phone screen",
  );
}
// A pinned first column with a transparent background still reports as sticky
// and still looks broken: the scrolling cells slide visibly underneath it.
// --color-bg does not exist in this theme; the page paints --color-ink.
{
  const flat = css.replace(/\s+/g, "");
  if (!/first-child\)?\{[^}]*position:sticky/.test(flat)) {
    fail.push("wide tables have no sticky first column — the row label scrolls away from its cells on mobile");
  } else if (/first-child\)?\{[^}]*background:var\(--color-bg\)/.test(flat)) {
    fail.push("sticky column uses `--color-bg`, which this theme does not define — it resolves to transparent and cells scroll under the label");
  }
  /* BOTH class names, because the fix was applied to one and not the other.
     .cmp got it; .patch -- the interactive routing matrix on the home page, the
     worse case at 403px of 736 hidden -- was missed for a full commit. A check
     naming only the class that was fixed would have passed throughout. */
  for (const cls of ["cmp", "patch"]) {
    if (!new RegExp(`\\.${cls}[,)][^{]*first-child`).test(flat)) {
      fail.push(`.${cls} tables are not in the sticky-first-column rule — their label column scrolls away on mobile`);
    }
  }
  /* TWO PSEUDO-ELEMENT AFFORDANCES, and both need a haystack wider than the
     CSS bundle plus a pattern looser than the source spelling.

     WHERE. Astro inlines a component's scoped styles into the pages that use
     it, so `.shot-open`'s rule is in features.html and in NO file under
     _astro/. A check reading only the bundle reports it missing on a build
     where it is present and working -- which is how the first version of this
     check failed, against a glyph already verified in a browser.

     HOW SPELLED. Lightning CSS rewrites `::after` to the CSS2 `:after`, which
     is two bytes shorter and identical in meaning. Matching the source
     spelling finds nothing in the output. `::?after` accepts either. */
  const built = flat + readdirSync(DIST)
    .filter((f) => f.endsWith(".html"))
    .map((f) => readFileSync(join(DIST, f), "utf8").replace(/\s+/g, ""))
    .join("");

  if (!/\.scroll-hint::?after\{/.test(built)) {
    fail.push("no `.scroll-hint::after` in the built output — wide tables give no sign they scroll on mobile");
  }
  /* The lightbox expand glyph. `cursor: zoom-in` is the only other affordance
     and it does not exist on a touch device -- which is the viewport where the
     screenshots are smallest and expanding them matters most. */
  if (!/\.shot-open::?after\{/.test(built)) {
    fail.push("no `.shot-open::after` in the built output — the lightbox is undiscoverable on touch, where `cursor: zoom-in` means nothing");
  }
}

/* Code blocks go through the CodeBlock component, not hand-rolled <pre>.
 *
 * There were three treatments before it existed -- radius 0, 4 and 8, three
 * backgrounds, two type sizes -- for the same kind of content on three pages of
 * one site. Nobody chose that. It is what happens when the styling lives at each
 * use site, and it will happen again the next time someone needs a code block
 * in a hurry.
 *
 * Checked against the BUILT pages rather than the source, so it sees what a
 * reader gets. The component emits `<pre>` inside `.code`; a hand-rolled one has
 * no such ancestor, and the class attribute Tailwind leaves on it is the tell.
 */
for (const f of readdirSync(DIST).filter((n) => n.endsWith(".html"))) {
  const html = readFileSync(join(DIST, f), "utf8");
  const handRolled = [...html.matchAll(/<pre\s+class="[^"]+"/g)];
  if (handRolled.length) {
    fail.push(
      `${f}: ${handRolled.length} hand-styled <pre> — use the CodeBlock component so every code block shares one radius, ground and size\n` +
        `    ${handRolled[0][0].slice(0, 100)}`,
    );
  }
}

// 3+4. Internal links and fragments must resolve to something that was built.
/* The motion tokens have to survive minification as VARIABLES.
 *
 * This file already exists because Lightning CSS folded `animation-timeline`
 * into a shorthand Chrome rejects, and the scroll reveals silently stopped
 * running in production. The same class of failure applies to a token: if the
 * minifier inlines `--motion-instant` at each use site, the scale still looks
 * right and stops being a scale — nothing can override it, which is exactly how
 * the app's reduced-motion block ended up decorative for months. */
if (!/--motion-instant:\s*90ms/.test(css)) {
  fail.push("the motion scale is missing from the built CSS: --motion-instant was not emitted");
}
if (!/var\(--motion-instant\)/.test(css)) {
  fail.push(
    "nothing REFERENCES --motion-instant in the built CSS — the minifier inlined it, " +
      "so the scale is decorative and cannot be overridden",
  );
}

/* The meter keyframe must stay asymmetric.
 *
 * A symmetric rise and fall is a sine wave, and to the audience this page is
 * written for that reads as a decorative web animation rather than an
 * instrument. Attack fast, decay slow — the check is that the midpoint is NOT
 * the extreme, which is what a 0/50/100 keyframe always is. */
/* NB: matched against the css as-built, NOT a whitespace-stripped copy. The
 * first version of this check stripped whitespace first, which turned
 * "@keyframes meter" into "@keyframesmeter" and matched nothing — the guard
 * passed on a deliberately symmetric keyframe. Caught by mutating the source
 * and watching the build stay green. */
const meterKf = /@keyframes\s+meter\s*\{((?:[^{}]*\{[^{}]*\})*)\s*\}/.exec(css);
if (!meterKf) {
  fail.push("the meter keyframe is gone from the built CSS; this check no longer guards anything");
} else if (/(^|[},])\s*50%\s*[,{]/.test(meterKf[1])) {
  fail.push(
    "the meter keyframe is symmetric again (has a 50% stop): real peak meters " +
      "attack fast and decay slowly, and engineers read a sine wobble as fake",
  );
}

const pages = readdirSync(DIST).filter((f) => f.endsWith(".html"));

/* Every nav page must mark ITSELF as the current one, in BOTH navs.
 *
 * This broke silently in the move to Cloudflare Workers. The build emits flat
 * files, so Astro.url.pathname is "/features.html" while the link href is
 * "/features" -- the comparison was false on every page and the nav marked
 * nothing at all. No aria-current for a screen reader, and the link for the
 * page you were already on painted the same muted grey as the others.
 *
 * It is checked here rather than trusted because of HOW it failed: a nav with
 * no highlight looks like a design decision, not a bug, so nothing about the
 * rendered page announces it. Two counts rather than one, because the desktop
 * nav and the mobile menu are separate markup and the mobile one -- the only
 * navigation a phone has -- was the half left behind when this was first fixed.
 */
for (const f of ["features", "comparison", "docs", "download"]) {
  if (!pages.includes(`${f}.html`)) continue;
  const html = readFileSync(join(DIST, `${f}.html`), "utf8");
  const marked = [...html.matchAll(new RegExp(`<a href="/${f}"[^>]*aria-current="page"`, "g"))].length;
  if (marked < 2) {
    fail.push(
      `${f}.html marks its own nav link as current ${marked} time(s), expected 2 ` +
        `(desktop nav + mobile menu) — check Astro.url.pathname is normalised against the flat-file output`,
    );
  }
}
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

/* 6. The comparison table is published three times and the copies must agree.
 *
 * docs/COMPARISON.md, /comparison and /features each carry their own copy of
 * the capability rows, and that duplication has now drifted three separate
 * times: the destination cap was corrected on the site and left wrong in the
 * two documents, the recording row said Restreamer records when their own
 * tracker says otherwise, and the same row was worded "Runs on your hardware"
 * in one copy and "Runs on hardware you control" in another. Prose asking
 * people to keep them in step has not worked once.
 *
 * This compares SHIPPED TEXT to SHIPPED TEXT: the rows a visitor's browser
 * actually renders, against the document those rows claim to come from. It is
 * deliberately not the anti-pattern in #107 — nothing here greps source code to
 * infer behaviour, and there is no assertion that passes because a string
 * happens to appear in a file nobody executes. If the built page says it, this
 * check reads it from the built page. */
const COMPARISON_MD = new URL("../../docs/COMPARISON.md", import.meta.url).pathname;

const ENTITIES = {
  amp: "&", lt: "<", gt: ">", quot: '"', nbsp: " ",
  mdash: "—", ndash: "–", hellip: "…",
  lsquo: "‘", rsquo: "’", ldquo: "“", rdquo: "”", "#39": "'",
};

/* Typography is the whole difficulty here. The Markdown says `**Multitrack
 * archive** — every…`, the built HTML says `Multitrack archive &#8212; every…`,
 * and the smart-quote transform turns "Yes" into “Yes” on the way. Normalise
 * both sides to the same plain lowercase text or this check false-positives on
 * its first run and gets deleted by the next person. */
const norm = (s) =>
  s
    .replace(/<[^>]*>/g, " ")
    .replace(/&([a-z]+|#\d+);/gi, (_, e) => ENTITIES[e.toLowerCase()] ?? " ")
    .replace(/[*`]/g, "")
    .replace(/[‐-―−]/g, "-")
    .replace(/[‘’]/g, "'")
    .replace(/[“”]/g, '"')
    .replace(/\s+/g, " ")
    .trim()
    .toLowerCase();

const tablesIn = (html) => html.match(/<table[\s\S]*?<\/table>/g) || [];
const rowsIn = (table) => table.match(/<tr[\s\S]*?<\/tr>/g) || [];
const cellsIn = (row) => (row.match(/<t[dh][\s\S]*?<\/t[dh]>/g) || []).map(norm);

const comparisonMd = norm(readFileSync(COMPARISON_MD, "utf8"));

/* Rows that exist on the site and deliberately NOT in docs/COMPARISON.md.
 * The document's capability table is headed "what polyemesis has that none of
 * them have", and this row is not one of those — Restreamer scores Yes on it
 * too. It earns its place in the site's table as the reason a reader who does
 * not care about audio might still be here. Adding a row to this list is fine;
 * doing it without saying why is not. */
const siteOnlyRows = new Set([
  "runs on hardware you control, no per-destination pricing",
]);

let capabilityRowsChecked = 0;
for (const f of pages) {
  const html = readFileSync(join(DIST, f), "utf8");
  for (const table of tablesIn(html)) {
    const rows = rowsIn(table);
    if (!rows.length || !cellsIn(rows[0]).includes("capability")) continue;
    for (const row of rows.slice(1)) {
      const label = cellsIn(row)[0];
      if (!label || siteOnlyRows.has(label)) continue;
      capabilityRowsChecked++;
      if (!comparisonMd.includes(label)) {
        fail.push(
          `${f}: capability row "${label}" does not appear in docs/COMPARISON.md.\n` +
          `    The page and the document have drifted. Reword whichever one is wrong ` +
          `— or, if the row is site-only on purpose, add it to siteOnlyRows here and say why.`,
        );
      }
    }
  }
}
if (!capabilityRowsChecked) {
  fail.push("no capability table found in the built pages; the COMPARISON.md parity check no longer guards anything");
}

/* Restreamer's recording cell specifically, because it shipped as a bare "Yes"
 * for months while datarhei/restreamer#692 — their own open request for exactly
 * this — sat open since February 2024. A row that is confidently wrong about a
 * competitor is the one that costs the page its credibility. */
let recordingRowChecked = false;
for (const table of tablesIn(readFileSync(join(DIST, "comparison.html"), "utf8"))) {
  const rows = rowsIn(table);
  if (!rows.length) continue;
  const column = cellsIn(rows[0]).indexOf("restreamer");
  if (column < 0) continue;
  for (const row of rows.slice(1)) {
    const cells = cellsIn(row);
    if (cells[0] !== "recording") continue;
    recordingRowChecked = true;
    if (cells[column] === "yes") {
      fail.push(
        `comparison.html: Restreamer's recording cell renders as a bare "Yes". ` +
        `It is an open feature request on their tracker (datarhei/restreamer#692) — ` +
        `see the footnote in docs/COMPARISON.md.`,
      );
    }
  }
}
if (!recordingRowChecked) {
  fail.push("no Recording row found on the built comparison page; that check no longer guards anything");
}

/* 8. `_headers` has to reach the root of the UPLOADED directory.
 *
 * Cloudflare reads exactly one path for response headers: `_headers` at
 * the top of the deployed output. Nothing else — not a subdirectory, not
 * public/_headers, which is a source path Pages never sees. Put it anywhere but
 * there and Pages ignores it in silence: no warning at deploy time, no error at
 * request time, a site that serves normally with no Content-Security-Policy on
 * anything.
 *
 * internal/testenv/pagesdeploy_test.go compares the header VALUES in
 * web/public/_headers against web/nginx-security-headers.conf. That comparison
 * is worth nothing if the file never arrives, and only the build knows whether
 * it did — Astro copying public/ into dist/ is a build behaviour, not a
 * declaration anyone can read off the source. This is the half that has to run
 * after a build. */
const headersPath = join(DIST, "_headers");
let headersText = "";
try {
  headersText = readFileSync(headersPath, "utf8");
} catch {
  fail.push(
    "no _headers at the root of dist/. Cloudflare Pages looks there and nowhere else, " +
    "so every security header the site claims to send would be silently absent. " +
    "It belongs in public/_headers, which Astro copies verbatim into dist/.",
  );
}
if (headersText && !/^\s+Content-Security-Policy:\s*\S/m.test(headersText)) {
  fail.push(
    "dist/_headers exists but sets no Content-Security-Policy. On nginx the CSP came " +
    "from nginx-security-headers.conf; Cloudflare Pages does not read that file, so " +
    "this is the only place it can come from.",
  );
}

if (fail.length) {
  console.error("build checks FAILED:\n" + fail.map((f) => "  - " + f).join("\n"));
  process.exit(1);
}
console.log(`build checks passed (${pages.length} pages, ${cssFiles.length} stylesheet)`);
