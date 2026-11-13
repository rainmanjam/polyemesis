// @ts-check
/* Renders the link-preview images: one 1200x630 PNG per card in src/data/og.mjs, drawn by
 * og/card.html, written to public/og/<slug>.png, with their hashes in og/manifest.txt.
 *
 *   npm run og                    render every card and rewrite the manifest
 *   node scripts/build-og.mjs --check   exit 1 if any card's data or the template changed since
 *                                       its PNG was rendered, or a PNG is missing; renders nothing
 *   node scripts/build-og.mjs --social  also write the repository's GitHub social preview,
 *                                       docs/media/social/social-preview.png (2560x1280), from
 *                                       the home card; not part of the manifest or --check
 *
 * WHEN TO RUN IT: after editing a card in src/data/og.mjs, a doc's title or description in
 * src/data/docs.mjs, or og/card.html. Also after changing a font file in public/fonts or
 * upgrading Playwright: --check compares card data and the template ONLY, so it cannot see
 * either and stays green while the pixels would differ. Commit the PNGs and og/manifest.txt it
 * changes. It also deletes any public/og/*.png that no longer has a card.
 *
 * DETERMINISTIC ON PURPOSE: no dates, no random values, device scale factor 1, the site's own
 * fonts from public/fonts, and the manifest only changes when a hash does, so re-running with
 * nothing changed leaves `git status` clean. Needs a Chromium: `npx playwright install chromium`.
 * Nothing here touches the network. */
import { mkdirSync, readFileSync, writeFileSync, existsSync, readdirSync, unlinkSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { OG_CARDS, formatOgManifest, ogHash, parseOgManifest } from "../src/data/og.mjs";

const root = (/** @type {string} */ p) => fileURLToPath(new URL(`../${p}`, import.meta.url));
const outDir = root("public/og");
const manifestPath = root("og/manifest.txt");
const template = readFileSync(root("og/card.html"), "utf8");
const want = Object.fromEntries(OG_CARDS.map((c) => [c.slug, ogHash(c, template)]));

const strayPngs = () =>
  existsSync(outDir) ? readdirSync(outDir).filter((f) => f.endsWith(".png") && !(f.slice(0, -4) in want)) : [];

const args = new Set(process.argv.slice(2));

if (args.has("--check")) {
  /** @type {Record<string, string>} */
  let have = {};
  try {
    have = parseOgManifest(readFileSync(manifestPath, "utf8"));
  } catch {
    /* no manifest: every card reports as stale below */
  }
  const stale = [];
  for (const c of OG_CARDS) {
    if (!existsSync(`${outDir}/${c.slug}.png`)) stale.push(`${c.slug}: public/og/${c.slug}.png is missing`);
    else if (have[c.slug] !== want[c.slug]) stale.push(`${c.slug}: card data or og/card.html changed since it was rendered`);
  }
  for (const f of strayPngs()) stale.push(`${f.slice(0, -4)}: public/og/${f} has no card; run \`npm run og\` to delete it`);
  for (const slug of Object.keys(have)) if (!(slug in want)) stale.push(`${slug}: in og/manifest.txt but no longer a card`);
  if (stale.length) {
    console.error(`link-preview images are out of date:\n  ${stale.join("\n  ")}\nRun \`npm run og\` in web/ and commit the result.`);
    process.exit(1);
  }
  console.log(`link-preview images are current (${OG_CARDS.length} cards)`);
  process.exit(0);
}

const cardUrl = (/** @type {object} */ card) => `${new URL(`file://${root("og/card.html")}`).href}?d=${encodeURIComponent(JSON.stringify(card))}`;

const browser = await chromium.launch();
try {
  /** @param {object} card @param {number} scale @param {number} w @param {number} h @param {string} file */
  const render = async (card, scale, w, h, file) => {
    const page = await browser.newPage({ viewport: { width: w, height: h }, deviceScaleFactor: scale });
    /** @type {Error[]} */
    const pageErrors = [];
    page.on("pageerror", (e) => pageErrors.push(e));
    await page.goto(cardUrl(card));
    await page.evaluate(() => document.fonts.ready);
    await page.waitForFunction(() => document.documentElement.dataset.ready === "1");
    if (pageErrors.length) throw new Error(`${file}: the card page threw: ${pageErrors.map((e) => e.message).join("; ")}`);
    const state = await page.evaluate(() => ({ ...document.documentElement.dataset }));
    if (state.overflow === "true" || state.clamped === "true") {
      throw new Error(`${file}: text does not fit (overflow=${state.overflow}, clamped=${state.clamped}, font ${state.fit}px)`);
    }
    await page.screenshot({ path: file });
    await page.close();
    return state.fit;
  };

  mkdirSync(outDir, { recursive: true });
  for (const c of OG_CARDS) {
    const fit = await render(c, 1, 1200, 630, `${outDir}/${c.slug}.png`);
    console.log(`${c.slug}.png  headline ${fit}px`);
  }
  for (const f of strayPngs()) {
    unlinkSync(`${outDir}/${f}`);
    console.log(`${f}  deleted (no card)`);
  }
  writeFileSync(manifestPath, formatOgManifest(want));

  if (args.has("--social")) {
    const home = OG_CARDS.find((c) => c.slug === "home");
    await render({ ...home, social: true }, 2, 1280, 640, root("../docs/media/social/social-preview.png"));
    console.log("docs/media/social/social-preview.png  2560x1280");
  }
} finally {
  await browser.close();
}
