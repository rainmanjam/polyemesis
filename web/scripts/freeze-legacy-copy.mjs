#!/usr/bin/env node
/* Freezes the copy of the old inner pages as a test fixture.
 *
 * Reads dist/<route>.html for the 12 routes the inner-page redesign rebuilds
 * and writes src/lib/legacy-inner.fixture.json: { h1, h2[], text } per route.
 * `text` is the text of <main> with script and style removed, normalised the
 * same way as norm() in check-build.mjs (tags removed, entities decoded,
 * whitespace collapsed, lower-cased); h1 and h2 keep their original case.
 *
 * Run once, after `npm run build`, on the OLD pages. It is never regenerated:
 * check-build.mjs asserts every sentence in the fixture is still on the
 * rebuilt page. The sentences that carried the retired phrase are removed
 * below on purpose, so that check never asks for them.
 */
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const DIST = new URL("../dist", import.meta.url).pathname;
const OUT = new URL("../src/lib/legacy-inner.fixture.json", import.meta.url).pathname;

const ROUTES = [
  "/comparison",
  "/vs/aitum-multistream",
  "/vs/obs-multi-rtmp",
  "/vs/restreamer",
  "/vs/streamelements",
  "/vs/streamlabs",
  "/multistream",
  "/free-restream-service",
  "/how-to-multistream-from-obs",
  "/twitch-vod-track",
  "/calculator",
  "/docs",
];

/* Normalised forms. The first two are the older wording the plan named; plan 1
 * already reworded the two limit rows, so the next two are what the built
 * pages say today. The older strings simply do not match and are harmless. */
const LEGACY_REMOVALS = [
  "restreamer is established. polyemesis is pre-release with one maintainer.",
  "here: pre-release, one maintainer.",
  "restreamer is established. polyemesis is still pre-release.",
  "here: still pre-release.",
];

/* Same table as check-build.mjs. */
const ENTITIES = {
  amp: "&", lt: "<", gt: ">", quot: '"', nbsp: " ",
  mdash: "—", ndash: "–", hellip: "…",
  lsquo: "‘", rsquo: "’", ldquo: "“", rdquo: "”", "#39": "'",
};

const decode = (s) =>
  s.replace(/&([a-z]+|#\d+);/gi, (_, e) => ENTITIES[e.toLowerCase()] ?? " ");

const norm = (s) =>
  decode(s.replace(/<[^>]*>/g, " "))
    .replace(/[*`]/g, "")
    .replace(/[‐-―−]/g, "-")
    .replace(/[‘’]/g, "'")
    .replace(/[“”]/g, '"')
    .replace(/\s+/g, " ")
    .trim()
    .toLowerCase();

/* Original case, same decoding, for the headings. */
const plain = (s) =>
  decode(s.replace(/<[^>]*>/g, " "))
    .replace(/\s+/g, " ")
    .trim();

const pageFile = (route) => join(DIST, `${route}.html`);

function freeze(route) {
  const html = readFileSync(pageFile(route), "utf8");
  const main = /<main[\s\S]*?<\/main>/.exec(html)?.[0];
  if (!main) throw new Error(`${route}: no <main>`);
  const body = main.replace(/<script[\s\S]*?<\/script>/g, " ").replace(/<style[\s\S]*?<\/style>/g, " ");
  const h1 = /<h1[\s\S]*?<\/h1>/.exec(body)?.[0];
  if (!h1) throw new Error(`${route}: no <h1>`);
  const h2 = (body.match(/<h2[\s\S]*?<\/h2>/g) || []).map(plain);
  let text = norm(body);
  for (const sentence of LEGACY_REMOVALS) text = text.split(sentence).join(" ");
  text = text.replace(/\s+/g, " ").trim();
  if (/one[- ]maintainer/i.test(text)) throw new Error(`${route}: still mentions "one maintainer"`);
  return { h1: plain(h1), h2, text };
}

const fixture = {};
for (const route of ROUTES) fixture[route] = freeze(route);
writeFileSync(OUT, JSON.stringify(fixture, null, 2) + "\n");
console.log(`froze ${ROUTES.length} routes into ${OUT}`);
