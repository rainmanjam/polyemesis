// @ts-check
import { createHash } from "node:crypto";
import { PUBLISHED, SECTIONS } from "./docs.mjs";
import { CASES } from "./cases.mjs";
import { DOCKER_RUN } from "./install.mjs";

/* THE LINK-PREVIEW CARDS: one per page, all drawn by web/og/card.html.
 *
 * Fixed routes are written by hand below, because a headline for a link
 * preview is copy, not a truncated <title>. Published docs are generated from
 * the manifest in docs.mjs, so a doc added there gets a card without anyone
 * remembering to ask for one. A route with no card here fails og.test.ts, which
 * computes its route list from `pageSource` in astro.config.mjs.
 *
 * COPY RULES THAT APPLY TO A CARD: outcome first, nothing self-deprecating, no
 * other product's name, platforms as plain text (no logos), "Stream Deck +".
 *
 * `accent` is one word of the title that the template sets in the serif italic
 * and coral. `visual` picks the drawing on the right or underneath:
 * "diagram" (one source, video and audio lines to language chips), "command"
 * (the install command, passed as `command` from install.mjs, never typed in the template), "table" (the site's sports example: a destination per row, a track per column, passed as `table`),
 * "none" (text only; `sub` carries the description).
 */

/** @typedef {{ columns: string[], rows: { name: string, cells: boolean[] }[] }} OgTable */

/**
 * @typedef {{
 *   slug: string, path: string, eyebrow?: string, title: string,
 *   accent?: string, sub?: string, command?: string, table?: OgTable,
 *   visual: "diagram" | "command" | "table" | "none",
 * }} OgCard
 */

/** The destinations-by-tracks grid, built from the homepage's own sports example (cases.mjs):
 *  the tracks are the columns, the destinations the rows, and a cell is on when the destination
 *  receives that track. All six tracks and all five destinations fit, so nothing is dropped.
 *  @returns {OgTable} */
const sportsTable = () => {
  /** @type {{ id: string, label: string }[]} */
  const tracks = CASES.sports.tracks;
  /** @type {{ name: string, tracks: string[] }[]} */
  const dests = CASES.sports.dests;
  return {
    columns: tracks.map((t) => t.label),
    rows: dests.map((d) => ({ name: d.name, cells: tracks.map((t) => d.tracks.includes(t.id)) })),
  };
};

/** `"/"` is `home`; every other path loses its leading slash and its inner slashes become hyphens.
 *  @param {string} path @returns {string} */
export const ogSlugFor = (path) => (path === "/" ? "home" : path.replace(/^\//, "").replace(/\//g, "-"));

/** @type {Omit<OgCard, "slug">[]} */
const FIXED = [
  {
    path: "/",
    title: "One stream in. Different audio out.",
    accent: "Different",
    visual: "diagram",
  },
  {
    path: "/features",
    eyebrow: "FEATURES",
    title: "Every destination, its own mix.",
    accent: "mix",
    table: sportsTable(),
    visual: "table",
  },
  {
    path: "/calculator",
    eyebrow: "CALCULATOR",
    title: "Know your bandwidth before you go live.",
    accent: "bandwidth",
    sub: "The upstream and server bandwidth a multistream needs, and the data it moves per day, week and month.",
    visual: "none",
  },
  {
    path: "/comparison",
    eyebrow: "COMPARISON",
    title: "The audio is the difference.",
    accent: "audio",
    table: sportsTable(),
    visual: "table",
  },
  {
    path: "/docs",
    eyebrow: "DOCS",
    title: "Setup, routing and API, versioned with the code.",
    accent: "routing",
    sub: "Setup, per-destination audio routing, operations and API reference for the self-hosted SRT restreaming server.",
    visual: "none",
  },
  {
    path: "/download",
    eyebrow: "INSTALL",
    title: "Run it on your own server.",
    accent: "own",
    command: DOCKER_RUN,
    visual: "command",
  },
  {
    path: "/free-restream-service",
    eyebrow: "SELF-HOSTED",
    title: "A free restream service you run yourself.",
    accent: "yourself",
    sub: "MIT licensed and self-hosted, with no cap on destinations.",
    visual: "none",
  },
  {
    path: "/how-to-multistream-from-obs",
    eyebrow: "GUIDE",
    title: "How to multistream from OBS.",
    accent: "multistream",
    sub: "Send one stream to Twitch, YouTube and Kick: a plugin, a hosted service, or a server you run.",
    visual: "none",
  },
  {
    path: "/multistream",
    eyebrow: "MULTISTREAM",
    title: "One upload to Twitch, YouTube and Kick.",
    accent: "upload",
    sub: "A different audio mix for every destination, from a single SRT ingest.",
    visual: "none",
  },
  {
    path: "/twitch-vod-track",
    eyebrow: "TWITCH VOD TRACK",
    title: "Different audio live and on the archive.",
    accent: "archive",
    sub: "A live mix and a separate VOD mix, from one SRT ingest and one video encode.",
    visual: "none",
  },
  {
    path: "/vs/aitum-multistream",
    eyebrow: "COMPARED",
    title: "Mix tracks on the server, per destination.",
    accent: "server",
    sub: "A plugin assigns one OBS audio track to each destination. polyemesis sums tracks server-side.",
    visual: "none",
  },
  {
    path: "/vs/obs-multi-rtmp",
    eyebrow: "COMPARED",
    title: "Upload once, fan out from a server.",
    accent: "once",
    sub: "Your uplink carries one stream instead of one RTMP output per destination.",
    visual: "none",
  },
  {
    path: "/vs/restreamer",
    eyebrow: "COMPARED",
    title: "A different mix per destination, on your own server.",
    accent: "mix",
    sub: "Self-hosted and MIT licensed, with audio mixed separately for every platform.",
    visual: "none",
  },
  {
    path: "/vs/streamelements",
    eyebrow: "COMPARED",
    title: "Multistream over SRT, from any operating system.",
    accent: "SRT",
    sub: "A server you run sends a different audio mix to each platform.",
    visual: "none",
  },
  {
    path: "/vs/streamlabs",
    eyebrow: "COMPARED",
    title: "Your own relay, your own mix.",
    accent: "mix",
    sub: "A relay you run yourself, with a different audio mix per destination.",
    visual: "none",
  },
];

/** @param {{ slug: string, canonical?: string }} d */
export const hasOtherCanonical = (d) => Boolean(d.canonical && d.canonical !== `/docs/${d.slug}`);

const SECTION_TITLE = new Map(SECTIONS.map((s) => [s.id, s.title]));

/** @type {OgCard[]} */
export const OG_CARDS = [
  ...FIXED.map((c) => ({ ...c, slug: ogSlugFor(c.path) })),
  // A doc that is canonicalised to another route (COMPARISON.md -> /comparison) is served with
  // that route's og:image, so a card of its own would ship unreferenced.
  ...PUBLISHED.filter((d) => !hasOtherCanonical(d)).map(
    (d) =>
      /** @type {OgCard} */ ({
        slug: ogSlugFor(`/docs/${d.slug}`),
        path: `/docs/${d.slug}`,
        eyebrow: `DOCS · ${(SECTION_TITLE.get(d.section) ?? d.section).toUpperCase()}`,
        title: d.title,
        sub: d.description,
        visual: "none",
      }),
  ),
];

/** The hash recorded in og/manifest.txt: the card's data and the template it is drawn with.
 *  Both change what the PNG looks like, so either one makes the file stale. The fonts are
 *  not hashed (nor is the Playwright version): after changing a font file or upgrading
 *  Playwright, re-run `npm run og` by hand. Line endings are normalised so a CRLF checkout
 *  hashes the same.
 *  @param {OgCard} card @param {string} template @returns {string} */
export function ogHash(card, template) {
  const data = JSON.stringify({
    slug: card.slug,
    path: card.path,
    eyebrow: card.eyebrow ?? null,
    title: card.title,
    accent: card.accent ?? null,
    sub: card.sub ?? null,
    command: card.command ?? null,
    table: card.table ?? null,
    visual: card.visual,
  });
  return createHash("sha256").update(data).update("\0").update(template.replace(/\r\n?/g, "\n")).digest("hex");
}

/** Slugs that have a card, for the page layout's fallback to `home`. */
export const OG_SLUGS = new Set(OG_CARDS.map((c) => c.slug));

/** The manifest on disk: one line per card, `<sha256>  <slug>`, the layout `shasum` prints.
 *  It is not JSON on purpose. As `"docs-api": "<64 hex characters>"` a line reads as a
 *  credential to a secret scanner (a name containing "api", a colon, a long random value),
 *  and the honest fix for a false alarm is a file that does not look like one, not an
 *  exemption in the scanner.
 *  @param {Record<string, string>} hashes slug to hash @returns {string} */
export function formatOgManifest(hashes) {
  return Object.entries(hashes)
    .map(([slug, hash]) => `${hash}  ${slug}\n`)
    .join("");
}

/** Reads what formatOgManifest wrote. A line that is not `<64 hex>  <slug>` is an error, not
 *  something to skip: a manifest that half-parses would report cards as stale for no reason.
 *  @param {string} text @returns {Record<string, string>} slug to hash */
export function parseOgManifest(text) {
  /** @type {Record<string, string>} */
  const hashes = {};
  for (const line of text.split(/\r?\n/)) {
    if (line === "") continue;
    const m = /^([0-9a-f]{64}) {2}(\S+)$/.exec(line);
    if (!m) throw new Error(`og/manifest.txt: not a "<sha256>  <slug>" line: ${line}`);
    hashes[m[2]] = m[1];
  }
  return hashes;
}
