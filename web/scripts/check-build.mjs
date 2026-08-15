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
import { PUBLISHED, NOT_PUBLISHED, SECTIONS, SECTION_IDS_WITH_DOCS } from "../src/data/docs.mjs";

const DIST = new URL("../dist/", import.meta.url).pathname;
const DOCS_SRC = new URL("../../docs/", import.meta.url).pathname;
const fail = [];

/* Every built page, INCLUDING the nested ones, as a path relative to dist/.
 *
 * This used to be `readdirSync(DIST).filter(endsWith(".html"))`, which is the
 * whole directory listing and reads like it covers the site. It covered the top
 * level only, and that was fine for exactly as long as every page lived there.
 *
 * TWO CHANGES ARRIVED AT THIS INDEPENDENTLY, and both are worth recording
 * because they fail in opposite directions.
 *
 * The /vs/* comparison pages were the first nested ones, and there the LOUD
 * failure was the internal-link check: /comparison links to
 * /vs/aitum-multistream, the page is absent from `built`, and the build fails
 * claiming a page that WAS built was not.
 *
 * The 23 rendered documentation pages build to dist/docs/<slug>.html, and there
 * the failure is silent and worse. The hand-rolled-<pre> ban is the guard the
 * whole docs change is organised around; rendering docs/*.md puts 114 more code
 * blocks on this site, and a flat listing would have exempted every one of them
 * from it -- reporting a pass while not looking. The amber reservation and the
 * _headers rule go the same way.
 *
 * A green check that has stopped looking is worse than a red one, which is the
 * reason this comment is this long.
 *
 * Forward slashes, because these are compared against URL paths. */
/** @returns {string[]} */
function htmlPages(dir = DIST, prefix = "") {
  /** @type {string[]} */
  const out = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    // `_astro` holds hashed assets and `.well-known` holds security.txt;
    // neither contains a page, and both are read directly where they matter.
    if (entry.name === "_astro" || entry.name.startsWith(".")) continue;
    if (entry.isDirectory()) {
      out.push(...htmlPages(join(dir, entry.name), `${prefix}${entry.name}/`));
    } else if (entry.name.endsWith(".html")) {
      out.push(prefix + entry.name);
    }
  }
  return out;
}

const pages = htmlPages();

/** The URL a built page is served at. `build.format: "file"` with
 * `trailingSlash: "never"` means docs/api.html is served as /docs/api.
 * @param {string} page @returns {string} */
const servedAt = (page) => "/" + page.replace(/\.html$/, "").replace(/(^|\/)index$/, "$1").replace(/\/$/, "");

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
  const built = flat + htmlPages().map((f) => readFileSync(join(DIST, f), "utf8").replace(/\s+/g, "")).join("");

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
 *
 * THE CLASS ATTRIBUTE IS NO LONGER THE ONLY TELL, and the second check below is
 * why. Rendering docs/*.md put 114 more code blocks on this site, produced by a
 * hast plugin rather than by the component, and it exposed how narrow this test
 * had always been: a `<pre>` with no class at all sails through it while looking
 * like nothing else on the page. Astro's own Markdown output is exactly that
 * once syntax highlighting is off. So the class check stays -- it names the
 * Shiki case precisely, and Shiki being switched back on is the realistic way
 * this regresses -- and a count invariant covers the plain one.
 */
for (const f of pages) {
  const html = readFileSync(join(DIST, f), "utf8");
  const handRolled = [...html.matchAll(/<pre\s+class="[^"]+"/g)];
  if (handRolled.length) {
    fail.push(
      `${f}: ${handRolled.length} hand-styled <pre> — use the CodeBlock component so every code block shares one radius, ground and size\n` +
        `    ${handRolled[0][0].slice(0, 100)}`,
    );
  }

  /* EVERY <pre> ON THE SITE IS INSIDE A CodeBlock, counted rather than parsed.
   *
   * The component emits exactly one `<pre>` per block, and exactly one Copy
   * button with it -- unless the block is the `quote` variant, which is a
   * competitor's configuration shown as evidence and deliberately has no Copy
   * button, because inviting someone to paste a rival's config into their
   * terminal is not what that block is for. So across a page:
   *
   *     <pre> == [data-code-copy] + .code-quote
   *
   * A hand-rolled <pre> breaks the equality whether or not it carries a class,
   * and so does a Copy button that lost its block. Counting is enough here and
   * an HTML parse would not be better: the thing being asserted is a ratio
   * between two markers that only this component produces together.
   *
   * BOTH MARKERS ARE MATCHED INSIDE A TAG, not anywhere in the file, and the
   * first version of this was not -- it counted a bare `data-code-copy`, and
   * Astro inlines scripts/code-copy.ts into every page, where the selector
   * string `[data-code-copy]` reads as one more button. Every page came up one
   * over. A marker that also appears in the code that CONSUMES it cannot be
   * counted loose. */
  const pre = (html.match(/<pre[\s>]/g) || []).length;
  const copy = (html.match(/<button[^>]*\bdata-code-copy\b/g) || []).length;
  const quote = (html.match(/class="[^"]*\bcode-quote\b/g) || []).length;
  if (pre !== copy + quote) {
    fail.push(
      `${f}: ${pre} <pre> but ${copy} copy button(s) and ${quote} quote block(s) — ` +
        `every code block on this site comes from the CodeBlock component (or the ` +
        `hast plugin that emits its markup for rendered markdown), and each one ` +
        `carries a Copy button unless it is the quote variant. A bare <pre> is the ` +
        `fourth code-block treatment this file exists to prevent.`,
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

/* The meter-like keyframe must stay asymmetric.
 *
 * A symmetric rise and fall is a sine wave, and to the audience this page is
 * written for that reads as a decorative web animation rather than an
 * instrument. Attack fast, decay slow — the check is that the midpoint is NOT
 * the extreme, which is what a 0/50/100 keyframe always is.
 *
 * NOW POINTED AT `loud-momentary`, which is the hero's momentary-loudness bar.
 * It used to be `meter`, the six-track hero fan-out; that hero was replaced by
 * the routing table and its keyframe went with it. The RULE did not change and
 * neither did the reason for it, so the guard followed the motion rather than
 * being deleted alongside the thing it happened to be watching — a check that
 * quietly stops guarding anything is worse than no check, which is exactly what
 * the `if (!kf)` arm below exists to prevent. */
/* NB: matched against the css as-built, NOT a whitespace-stripped copy. The
 * first version of this check stripped whitespace first, which turned
 * "@keyframes meter" into "@keyframesmeter" and matched nothing — the guard
 * passed on a deliberately symmetric keyframe. Caught by mutating the source
 * and watching the build stay green. */
const meterKf = /@keyframes\s+loud-momentary\s*\{((?:[^{}]*\{[^{}]*\})*)\s*\}/.exec(css);
if (!meterKf) {
  fail.push(
    "the loud-momentary keyframe is gone from the built CSS; this check no longer guards anything",
  );
} else if (/(^|[},])\s*50%\s*[,{]/.test(meterKf[1])) {
  fail.push(
    "the loud-momentary keyframe is symmetric again (has a 50% stop): real meters " +
      "attack fast and decay slowly, and engineers read a sine wobble as fake",
  );
}

/* Every built page must have a revalidation rule in _headers.
 *
 * THIS GUARDS A BUG THAT SHIPPED. The block scoped itself to `/` and `/*.html`
 * and its comment claimed that covered "the built pages". It did not:
 * astro.config.mjs builds features.html but SERVES /features, and Cloudflare
 * matches the request path, so four of six pages went out with no `no-cache` at
 * all. The stated failure mode -- "a deploy is invisible until caches expire" --
 * was live on /features, /comparison, /docs and /download.
 *
 * Derived from the built output rather than a hand-list, so adding a page and
 * forgetting the header fails here rather than in production six hours later.
 *
 * IT NOW UNDERSTANDS PREFIXES, because 23 documentation pages arrived under
 * /docs/ and the alternative was 23 literal lines in _headers that the 24th
 * document would silently not get. `/docs/*` covers them.
 *
 * WHICH MEANS IT HAS TO READ THE FILE PROPERLY RATHER THAN GREP IT. The previous
 * version collected every line starting with `/` and compared for equality, and
 * `/*` -- the security-header block that must stay on everything -- never equals
 * a page path, so it was harmless. Under prefix matching `/*` matches EVERY
 * path, and this check would have passed on a file with no cache rules at all.
 * So only the rules that actually set Cache-Control are collected. The remaining
 * hole, someone putting Cache-Control back on `/*`, is the one
 * TestNoTwoHeaderRulesSetTheSameHeader in internal/testenv fails on. */
{
  const headers = readFileSync(join(DIST, "_headers"), "utf8");

  /** Path rules that set Cache-Control, in file order. */
  const cacheRules = [];
  {
    let path = null;
    for (const line of headers.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed || trimmed.startsWith("#")) continue;
      // A path rule starts at column zero; a header is indented under it.
      if (!/^[ \t]/.test(line)) {
        path = trimmed;
        continue;
      }
      if (path && /^cache-control\s*:/i.test(trimmed)) cacheRules.push(path);
    }
  }
  if (!cacheRules.length) {
    fail.push(
      "dist/_headers declares no Cache-Control rules at all, so the coverage check " +
        "below is comparing every page against an empty list and passing.",
    );
  }

  /* Cloudflare's `*` matches the rest of the path after the prefix it follows,
     so `/docs/*` covers /docs/api and not /docs itself -- which is why the
     literal `/docs` line is still in the file beside it. */
  const covered = (path) =>
    cacheRules.some((r) => (r.endsWith("*") ? path.startsWith(r.slice(0, -1)) : r === path));

  for (const f of pages) {
    const pretty = servedAt(f);
    if (!covered(pretty)) {
      fail.push(
        `web/public/_headers has no rule for ${pretty} — the page is SERVED at that ` +
          `path (trailingSlash: "never"), so \`/*.html\` does not match it and a ` +
          `deploy stays invisible until caches expire`,
      );
    }
  }
}

/* security.txt must not be expired, and must not be about to be.
 *
 * RFC 9116 makes Expires mandatory, and an expired file is worse than none: it
 * tells a researcher the channel is unmaintained at the exact moment they are
 * deciding between reporting privately and going public. 30 days of warning is
 * enough to renew without a scramble. */
{
  const sec = readFileSync(join(DIST, ".well-known", "security.txt"), "utf8");
  const m = /^Expires:\s*(\S+)/m.exec(sec);
  if (!m) {
    fail.push(".well-known/security.txt has no Expires field, which RFC 9116 requires");
  } else {
    const days = (Date.parse(m[1]) - Date.now()) / 86400000;
    if (Number.isNaN(days)) {
      fail.push(`.well-known/security.txt Expires is not a parseable date: ${m[1]}`);
    } else if (days < 0) {
      fail.push(
        `.well-known/security.txt EXPIRED ${Math.abs(Math.round(days))} days ago — ` +
          `renew it or remove the file; an expired contact reads as an abandoned one`,
      );
    } else if (days < 30) {
      fail.push(
        `.well-known/security.txt expires in ${Math.round(days)} days — renew it now, ` +
          `while this is a build failure rather than a live dead end`,
      );
    }
  }
}

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
/* 3+4, and the rewritten documentation links are the reason this now matters
 * far more than it did.
 *
 * docs/*.md carries 73 sibling links written as `](AUDIO-ROUTING.md)`, which
 * resolve on GitHub and point at nothing here. src/lib/mdast-doc-links.mjs
 * rewrites them to /docs/<slug>, and a rewrite that silently 404s is worse than
 * the GitHub link it replaced -- the reader had something that worked before.
 * This is the check that says the rewriting landed: the links are read back off
 * the built HTML and matched against the pages that were actually built, so it
 * verifies the OUTPUT rather than trusting the transform. Its other half lives
 * in the plugin, which fails the build when a link resolves to a repo path that
 * does not exist -- between the two, every link on a docs page is verified.
 *
 * The fragment pattern had to widen with the paths: it read `href="(\/[a-z]*)#..."`,
 * which cannot match /docs/audio-routing#mix-matrix, so every cross-document
 * anchor -- the majority of the 73 -- would have been skipped in silence. */
const built = new Set(pages.map(servedAt));
for (const f of pages) {
  const html = readFileSync(join(DIST, f), "utf8");
  for (const m of html.matchAll(/href="(\/[^"#?]*)(#[^"]*)?"/g)) {
    const path = m[1].replace(/\/$/, "") || "/";
    if (path.startsWith("/_astro") || /\.[a-z0-9]+$/i.test(path)) continue;
    if (!built.has(path)) fail.push(`${f}: links to ${m[1]}, which was not built`);
  }
  for (const m of html.matchAll(/href="(\/[a-z0-9/-]*)#([^"]+)"/g)) {
    const target = m[1] === "/" ? "index.html" : `${m[1].slice(1)}.html`;
    if (!pages.includes(target)) continue;
    // Heading ids come from github-slugger and can carry dots and underscores,
    // so the id is compared literally rather than through a character class.
    if (!readFileSync(join(DIST, target), "utf8").includes(`id="${m[2]}"`)) {
      fail.push(`${f}: links to ${m[1]}#${m[2]} but ${target} has no element with that id`);
    }
  }

  /* SAME-PAGE FRAGMENTS, which this check never looked at.
   *
   * The pattern above requires a path, so `href="#the-problem"` matched nothing
   * and a bare fragment was unverified on every page the site has ever had. It
   * went unnoticed because the five hand-written pages have a handful between
   * them and their authors could see the headings they were linking to.
   *
   * The documents cannot be read that way: they carry 75 of these, several are
   * hand-written contents lists at the top of a long page, and the id they point
   * at is generated from heading text by github-slugger. Reword a heading and
   * every link to it becomes a click that does nothing -- no console error, no
   * 404, the page simply stays where it is. */
  for (const m of html.matchAll(/href="#([^"]+)"/g)) {
    if (!html.includes(`id="${m[1]}"`)) {
      fail.push(`${f}: links to #${m[1]} but the page has no element with that id`);
    }
  }
}

/* THE PUBLISH LIST IS AN ALLOWLIST, AND THIS IS WHAT MAKES THAT TRUE.
 *
 * docs/ holds 37 markdown files. 23 are user-facing and now ship as pages; 14
 * are internal, and two of those would genuinely hurt -- RESEARCH-COMPETITIVE.md
 * is a survey of competitors' issue trackers, and COPY-CONSTRAINTS.md records
 * what this project's marketing copy may and may not claim. Publishing either is
 * not a broken page. It is a self-inflicted wound, and nobody here would notice
 * until somebody else did.
 *
 * The manifest in src/data/docs.mjs is a pair of lists rather than a glob with
 * exclusions, and the FIRST check below is the one that earns the difference:
 * the two lists have to partition docs/*.md exactly. Add a file to that
 * directory and the build fails until it is classified. A glob would have made
 * the same file public by default and said nothing.
 *
 * The second is the same claim from the other end -- what actually reached
 * dist/ -- because a manifest is a statement of intent and this file's whole
 * premise is that intent and output are different things. */
{
  const onDisk = readdirSync(DOCS_SRC)
    .filter((f) => f.endsWith(".md"))
    .sort();
  const classified = new Map();
  for (const d of PUBLISHED) classified.set(d.file, "published");
  for (const d of NOT_PUBLISHED) classified.set(d.file, "withheld");

  for (const f of onDisk) {
    if (!classified.has(f)) {
      fail.push(
        `docs/${f} is in neither PUBLISHED nor NOT_PUBLISHED in web/src/data/docs.mjs.\n` +
          `    Every file in docs/ has to be classified, so that publishing one is a ` +
          `decision somebody wrote down rather than the default. Add it to PUBLISHED ` +
          `with a hand-written title and description, or to NOT_PUBLISHED with the reason.`,
      );
    }
  }
  for (const f of classified.keys()) {
    if (!onDisk.includes(f)) {
      fail.push(
        `web/src/data/docs.mjs names docs/${f}, which does not exist. A renamed ` +
          `document leaves a page 404ing or a withheld file unguarded.`,
      );
    }
  }

  /* THE SLUG IS DERIVED NOW, so the thing that used to be a typo is a rule, and
     the rule needs a guard the typo never had.
     `slugOf` lowercases the filename and drops the extension, which is exactly
     right for the 23 SHOUTING-KEBAB.md files here and produces something no one
     wants for a filename shaped differently: `NOTES_v2.md` becomes
     `/docs/notes_v2`, and `CHANGES-SINCE-v0.6.0.md` becomes a URL with a dot in
     it that this flat-file build would emit as `changes-since-v0.6.0.html`.
     Neither is a crash. Both are a bad address nobody would notice until it was
     linked. */
  for (const d of PUBLISHED) {
    if (!/^[a-z0-9]+(-[a-z0-9]+)*$/.test(d.slug)) {
      fail.push(
        `docs/${d.file} derives the slug "${d.slug}", which is not a clean URL segment.\n` +
          `    slugOf() lowercases the filename and drops ".md"; that only gives a good ` +
          `address for SHOUTING-KEBAB-CASE names. Rename the document, or give it an ` +
          `explicit slug in web/src/data/docs.mjs.`,
      );
    }
  }

  /* Every section has a table, and every table belongs to a section.
     The two halves are separate structures — SECTION_ROWS carries the order and
     the copy, DOCS_BY_SECTION carries the membership — and the failure mode of
     splitting them is silent: a section listed with no table renders as a
     heading with nothing under it, and a table keyed to a section that does not
     exist drops every document in it off the site without a word. */
  {
    const ordered = SECTIONS.map((s) => s.id);
    for (const id of ordered) {
      if (!SECTION_IDS_WITH_DOCS.includes(id)) {
        fail.push(`section "${id}" is in SECTIONS but has no table in DOCS_BY_SECTION — it would render empty`);
      }
    }
    for (const id of SECTION_IDS_WITH_DOCS) {
      if (!ordered.includes(id)) {
        fail.push(
          `DOCS_BY_SECTION has a "${id}" table and SECTIONS does not list it — ` +
            `every document in it is silently dropped, because PUBLISHED is built from SECTIONS' order`,
        );
      }
    }
  }

  const wantSlugs = new Set(PUBLISHED.map((d) => `/docs/${d.slug}`));
  const gotSlugs = new Set(pages.map(servedAt).filter((p) => p.startsWith("/docs/")));
  for (const p of gotSlugs) {
    if (!wantSlugs.has(p)) fail.push(`${p} was built and is not in the docs allowlist — how did it get there?`);
  }
  for (const p of wantSlugs) {
    if (!gotSlugs.has(p)) fail.push(`${p} is in the docs allowlist and was NOT built`);
  }

  /* And the wound named directly. The set comparison above proves no extra PAGE
     was built; this proves no withheld document's CONTENT reached the site by
     some other route -- an inline include, a copied excerpt, a glob that got
     past the loader. Matched on the H1, which is the one line in each of these
     files that is both distinctive and stable. */
  for (const { file } of NOT_PUBLISHED) {
    const h1 = readFileSync(join(DOCS_SRC, file), "utf8").split("\n")[0].replace(/^#\s*/, "").trim();
    if (!h1) continue;
    for (const f of pages) {
      const html = readFileSync(join(DIST, f), "utf8");
      if (new RegExp(`<h1[^>]*>\\s*${h1.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`).test(html)) {
        fail.push(`${f}: carries the heading of docs/${file}, which is deliberately NOT published`);
      }
    }
  }
}

/* A PAGE WITH COPY BUTTONS MUST ALSO SHIP THE CODE THAT MAKES THEM WORK.
 *
 * THIS GUARDS A BUG THAT WAS LIVE IN THIS COMMIT'S OWN FIRST DRAFT. The copy
 * behaviour lives in scripts/code-copy.ts, imported by CodeBlock.astro, and
 * Astro bundles it into any page that uses the component. The documentation
 * pages never use the component -- lib/hast-code-block.mjs emits its markup
 * from the markdown -- so the script was not pulled in, and 114 buttons across
 * 23 pages rendered perfectly and did nothing at all when clicked.
 *
 * It is the worst shape of failure this repository keeps writing checks for: the
 * button is THERE, styled, focusable and labelled, so the page looks complete.
 * Nothing in the build, the HTML or the console says otherwise. It was found by
 * clicking one in a browser, which is not a thing CI does.
 *
 * Checked by looking for the attribute the handler binds to, rather than for a
 * filename: the emitted name is content-hashed, and a check asserting on a hash
 * breaks on every unrelated edit and gets deleted.
 *
 * IT HAS TO FOLLOW THE IMPORT GRAPH, and the first version did not. Two
 * shapes defeated it. Astro inlines a script below a size threshold, so the
 * same import is `<script>…</script>` on one page and a hashed module on
 * another. And a page's `<script src=…>` is frequently a one-line stub --
 * literally `import"./code-copy.C-9sLLhJ.js";` on index.html -- with the code in
 * a chunk shared between the pages that need it. Reading only the file the page
 * names finds nothing and reports every page broken, including the ones that
 * have always worked. */
for (const f of pages) {
  const html = readFileSync(join(DIST, f), "utf8");
  if (!/<button[^>]*\bdata-code-copy\b/.test(html)) continue;

  const inlined = [...html.matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/g)].some((m) =>
    m[1].includes("data-code-copy"),
  );

  /** Does this module, or anything it imports, bind the copy handler?
   * @param {string} src @param {Set<string>} seen @returns {boolean} */
  const reachable = (src, seen = new Set()) => {
    if (seen.has(src)) return false;
    seen.add(src);
    let js;
    try {
      js = readFileSync(join(DIST, "_astro", src), "utf8");
    } catch {
      return false;
    }
    if (js.includes("data-code-copy")) return true;
    return [...js.matchAll(/from\s*"\.\/([^"]+)"|import\s*"\.\/([^"]+)"/g)].some((m) =>
      reachable(m[1] ?? m[2], seen),
    );
  };

  const bundles = [...html.matchAll(/<script[^>]+src="\/_astro\/([^"]+\.js)"/g)].map((m) => m[1]);
  const wired = inlined || bundles.some((src) => reachable(src));
  if (!wired) {
    fail.push(
      `${f}: has Copy buttons but ships no script that reads them — every one of ` +
        `them renders correctly and does nothing when clicked.\n` +
        `    A page whose code blocks come from the hast plugin rather than from ` +
        `CodeBlock.astro has to import src/scripts/code-copy.ts itself.`,
    );
  }
}

/* EVERY DOCUMENTATION PAGE ACTUALLY RENDERED ITS DOCUMENT.
 *
 * THIS GUARDS A GREEN BUILD THAT SHIPPED NOTHING. Astro's glob loader catches
 * whatever a markdown pipeline throws, logs `[ERROR] Error rendering FOO.md`,
 * and CARRIES ON -- `astro build` exits 0, the page is emitted, and the page is
 * empty. Found by mutation: a deliberately broken cross-document link made
 * src/lib/mdast-doc-links.mjs throw by design, and the build reported success
 * while /docs/quickstart went out as chrome around a hole.
 *
 * So the plugin's throw is a diagnosis, not a gate. This is the gate. It is
 * deliberately about the OUTPUT rather than about links: any render failure --
 * a plugin, a malformed table, a loader change -- has the same symptom, and this
 * catches the ones nobody has thought of yet.
 *
 * If this fires, the reason is upstream in the build log, above the summary. */
for (const f of pages.filter((p) => p.startsWith("docs/"))) {
  const html = readFileSync(join(DIST, f), "utf8");
  const article = /<article[^>]*doc-prose[^>]*>([\s\S]*?)<\/article>/.exec(html);
  const text = article ? article[1].replace(/<[^>]*>/g, " ").replace(/\s+/g, " ").trim() : "";
  // The shortest published document is QUICKSTART at ~965 words. 500 characters
  // is far below anything real and far above the empty string a failed render
  // leaves behind, so this cannot fire on a genuinely short document.
  if (text.length < 500) {
    fail.push(
      `${f}: rendered ${text.length} characters of documentation, which means the ` +
        `markdown did not render.\n` +
        `    Astro's content loader logs a render failure and EXITS 0 — look for ` +
        `"Error rendering" earlier in this build's output for the real cause.`,
    );
  }
}

/* EXACTLY ONE <h1> PER PAGE.
 *
 * Verified by hand once, during an audit of the six original pages, and worth
 * keeping now that 23 more arrive from markdown nobody writes a layout for. A
 * document's own `# Title` becomes the page's h1; a layout that also renders one
 * would give every documentation page two, and the failure is invisible on the
 * page -- two headings that look like a title and a subtitle. */
for (const f of pages) {
  const n = (readFileSync(join(DIST, f), "utf8").match(/<h1[\s>]/g) || []).length;
  if (n !== 1) fail.push(`${f}: ${n} <h1> elements, expected exactly 1`);
}

// 5. The amber reservation, enforced rather than merely documented.
//
//    Amber (--color-cross / #e8a33d) marks the signal path: a lit crosspoint,
//    the route it feeds, and that route running hot. The rule drifted once
//    already — it said "crosspoint and nothing else" while the hero's routes
//    had always been amber, and a comparison-page callout had quietly picked it
//    up as a general accent. Prose in a stylesheet did not stop that; this does.
const amberAllowed = {
  // Zero, since the hero stopped drawing the signal path. The three uses this
  // counted were the route lines and their junction dot in the old hero SVG,
  // which the routing-table hero replaced — the fan-out it drew is still on the
  // page at full size in MixMatrix, which gets its amber from the .xpt rules
  // rather than from literals, so nothing here needs an allowance any more.
  //
  // Left as an explicit 0 rather than deleted: the entry is the record that the
  // hero USED to be the amber consumer, so a future literal appearing in
  // index.html reads as the drift this check exists to catch, not as a gap
  // somebody forgot to fill.
  "index.html": 0,
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
