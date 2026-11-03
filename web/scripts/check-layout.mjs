#!/usr/bin/env node
/* Card alignment, measured in a real browser against the BUILT site.
 *
 * check-build.mjs reads dist/ as text, and says so: "a static file cannot prove
 * no page overflows on a phone -- that needs a browser and a real layout". Card
 * alignment is the same kind of claim. Whether two cards in a row end at the
 * same height, whether a grid's right edge lands on the column's right edge,
 * whether a "Read more" line sits on the same baseline as its neighbour's --
 * none of that is in the HTML. It is in the layout, so this loads every page
 * that has a card grid in Chromium and measures it.
 *
 * WHAT IT HOLDS, per `.card-grid` (the one layout primitive in global.css), at
 * four viewport widths (rule 1 also for every bordered `.scroll-hint` table):
 *
 *   1. EDGES. The grid's left and right edges sit on the content column's
 *      edges, within 1px. The column is the `.wrap` content box, or the
 *      article on a documentation page. This is the rule a `max-w-5xl` on one
 *      grid broke on five pages: the row stopped 216px short of the grid above
 *      it on the same page.
 *   2. ROWS. Cards sharing a row have the same height, within 1px.
 *   3. BASELINE. Every `.card-foot` in a row ends on the same line,
 *      within 1px -- each card is a flex column and the foot is pushed to its
 *      bottom, so text of a different length above it cannot move it.
 *   4. NO ORPHAN. A last row with fewer cards than the first must still reach
 *      the grid's right edge (a deliberate full-width card does). A lone card
 *      beside a hole fails.
 *   5. ONE GUTTER. The gap between adjacent cards is the primitive's gap,
 *      read from the grid's own computed style, and it is the same value on
 *      every grid on the site.
 *   6. NO SIDEWAYS SCROLL. The document is no wider than the viewport.
 *
 * GREEN OVER NOTHING. A selector that stops matching makes every assertion
 * above vacuously true, so the pages the alignment work was done on carry a
 * minimum grid count, and the run as a whole a minimum number of rows
 * measured. A rename that empties the selector fails here instead of passing.
 *
 * Needs a Chromium: `npx playwright install chromium` once. Run after
 * `npm run build`.
 */
import { createServer } from "node:http";
import { createReadStream, existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { extname, join, resolve, sep } from "node:path";
import { chromium } from "playwright";

const DIST = resolve(new URL("../dist/", import.meta.url).pathname);
const WIDTHS = [1440, 1024, 768, 390];
const TOLERANCE = 1;

/* The pages the alignment work covered, and how many card grids each has at
 * minimum. Lower a number only alongside the markup change that removes a
 * grid; raising one is always safe. */
const REQUIRED = {
  "/": 1,
  "/features": 1,
  "/comparison": 3,
  "/download": 1,
  "/docs/install": 1,
};

const TYPES = {
  ".html": "text/html; charset=utf-8",
  ".css": "text/css",
  ".js": "text/javascript",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".webp": "image/webp",
  ".avif": "image/avif",
  ".woff2": "font/woff2",
  ".ico": "image/x-icon",
};

/** @returns {string[]} dist-relative paths of every built page */
function htmlPages(dir = DIST, prefix = "") {
  const out = [];
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    if (e.name === "_astro" || e.name.startsWith(".")) continue;
    if (e.isDirectory()) out.push(...htmlPages(join(dir, e.name), `${prefix}${e.name}/`));
    else if (e.name.endsWith(".html")) out.push(prefix + e.name);
  }
  return out;
}
const servedAt = (p) => "/" + p.replace(/\.html$/, "").replace(/(^|\/)index$/, "$1").replace(/\/$/, "");

/* Only pages that ship the primitive are worth a browser load. Read from the
 * built HTML so a page that gains a grid is measured without editing this. */
const pages = htmlPages()
  .filter((p) => /class="[^"]*\b(card-grid|scroll-hint)\b/.test(readFileSync(join(DIST, p), "utf8")))
  .map(servedAt);
for (const route of Object.keys(REQUIRED)) {
  if (!pages.includes(route)) pages.push(route);
}

function serve() {
  return new Promise((done) => {
    const srv = createServer((req, res) => {
      const path = decodeURIComponent(new URL(req.url ?? "/", "http://x").pathname);
      const base = resolve(DIST, "." + path);
      if (base !== DIST && !base.startsWith(DIST + sep)) {
        res.writeHead(403).end();
        return;
      }
      const file = [base, `${base}.html`, join(base, "index.html")].find((f) => existsSync(f) && statSync(f).isFile());
      if (!file) {
        res.writeHead(404).end();
        return;
      }
      res.writeHead(200, { "content-type": TYPES[extname(file)] ?? "application/octet-stream" });
      createReadStream(file).pipe(res);
    });
    srv.listen(0, "127.0.0.1", () => done(srv));
  });
}

/* Runs in the page. Returns plain numbers; every judgement is made outside. */
function measure() {
  const px = (v) => parseFloat(v) || 0;
  const columnOf = (grid) => {
    const article = grid.closest("[data-doc-column]")?.querySelector("article.doc-prose");
    if (article) {
      const b = article.getBoundingClientRect();
      return { left: b.left, right: b.right, host: "article" };
    }
    const wrap = grid.closest(".wrap");
    if (!wrap) return null;
    const b = wrap.getBoundingClientRect();
    const cs = getComputedStyle(wrap);
    return { left: b.left + px(cs.paddingLeft), right: b.right - px(cs.paddingRight), host: "wrap" };
  };
  // Bordered table wrappers (.scroll-hint) are held to the same edges: two of
  // the five short rows were tables capped at max-w-5xl.
  // A table inside a list item follows the item's text indent, as the item's
  // paragraphs do; its column is the list, so it is not held to the page's.
  const grids = [...document.querySelectorAll(".card-grid, .scroll-hint")].filter(
    (g) => g.getClientRects().length > 0 && !(g.classList.contains("scroll-hint") && g.closest("li")),
  );
  return {
    overflow: document.documentElement.scrollWidth - window.innerWidth,
    grids: grids.map((g) => {
      const b = g.getBoundingClientRect();
      const cs = getComputedStyle(g);
      // The content box: a grid may BE the .wrap (/download), padding and all.
      const left = b.left + px(cs.borderLeftWidth) + px(cs.paddingLeft);
      const right = b.right - px(cs.borderRightWidth) - px(cs.paddingRight);
      const items = [...g.children].filter((c) => c.getClientRects().length > 0);
      return {
        label: g.getAttribute("aria-label") || g.id || (g.previousElementSibling?.textContent ?? "").trim().slice(0, 40) || g.className,
        isCardGrid: g.classList.contains("card-grid"),
        left,
        right,
        column: columnOf(g),
        columnGap: px(cs.columnGap),
        items: items.map((c) => {
          const r = c.getBoundingClientRect();
          const ctas = [...c.querySelectorAll(".card-foot")].map((a) => a.getBoundingClientRect().bottom);
          return { top: r.top, left: r.left, right: r.right, height: r.height, cta: ctas.length ? Math.max(...ctas) : null };
        }),
      };
    }),
  };
}

const fail = [];
let rowsMeasured = 0;
const gaps = new Map();
const srv = await serve();
const origin = `http://127.0.0.1:${srv.address().port}`;
const browser = await chromium.launch();
try {
  for (const width of WIDTHS) {
    const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
    const page = await ctx.newPage();
    for (const route of pages) {
      const res = await page.goto(origin + route, { waitUntil: "load" });
      if (!res || !res.ok()) {
        fail.push(`${route}: HTTP ${res?.status()}`);
        continue;
      }
      // Lazy images change heights as they arrive; load them all first.
      await page.evaluate(async () => {
        for (const img of document.querySelectorAll("img[loading=lazy]")) img.loading = "eager";
        await Promise.all(
          [...document.images].map((i) => (i.complete ? 0 : new Promise((r) => { i.onload = i.onerror = r; }))),
        );
        await document.fonts.ready;
      });
      const m = await page.evaluate(measure);
      const at = `${route} @${width}`;

      if (m.overflow > 0) fail.push(`${at}: the page scrolls sideways by ${m.overflow}px`);
      const cardGrids = m.grids.filter((g) => g.isCardGrid).length;
      if (width === WIDTHS[0] && REQUIRED[route] && cardGrids < REQUIRED[route]) {
        fail.push(`${at}: ${cardGrids} .card-grid found, expected at least ${REQUIRED[route]} -- did the class move?`);
      }

      for (const g of m.grids) {
        const name = `${at} "${g.label}"`;
        if (!g.column) {
          fail.push(`${name}: not inside a .wrap or a documentation column`);
          continue;
        }
        const dl = g.left - g.column.left;
        const dr = g.column.right - g.right;
        if (Math.abs(dl) > TOLERANCE || Math.abs(dr) > TOLERANCE) {
          fail.push(`${name}: grid edges are ${dl.toFixed(1)}px / ${dr.toFixed(1)}px off the ${g.column.host} column (left / right)`);
        }
        if (!g.isCardGrid) continue;
        if (g.items.length > 1) gaps.set(g.columnGap, `${name}`);

        const rows = [];
        for (const it of g.items) {
          const row = rows.find((r) => Math.abs(r[0].top - it.top) <= TOLERANCE);
          if (row) row.push(it);
          else rows.push([it]);
        }
        rows.forEach((row, i) => {
          row.sort((a, b) => a.left - b.left);
          if (row.length > 1) rowsMeasured++;
          const hs = row.map((c) => c.height);
          if (Math.max(...hs) - Math.min(...hs) > TOLERANCE) {
            fail.push(`${name} row ${i + 1}: card heights differ (${hs.map((h) => h.toFixed(0)).join(" / ")})`);
          }
          const ctas = row.map((c) => c.cta).filter((c) => c != null);
          if (ctas.length > 1 && Math.max(...ctas) - Math.min(...ctas) > TOLERANCE) {
            fail.push(`${name} row ${i + 1}: .card-foot baselines differ by ${(Math.max(...ctas) - Math.min(...ctas)).toFixed(1)}px`);
          }
          for (let k = 1; k < row.length; k++) {
            const gap = row[k].left - row[k - 1].right;
            if (Math.abs(gap - g.columnGap) > TOLERANCE) {
              fail.push(`${name} row ${i + 1}: ${gap.toFixed(1)}px between cards, the grid's gap is ${g.columnGap}px -- spacing by margin?`);
            }
          }
        });
        if (rows.length > 1) {
          const last = rows[rows.length - 1];
          const lastRight = Math.max(...last.map((c) => c.right));
          if (last.length < rows[0].length && g.right - lastRight > TOLERANCE) {
            fail.push(`${name}: last row has ${last.length} of ${rows[0].length} cards and stops ${(g.right - lastRight).toFixed(0)}px short -- an orphan`);
          }
        }
      }
    }
    await ctx.close();
  }
} finally {
  await browser.close();
  srv.close();
}

if (gaps.size > 1) {
  fail.push(`card grids use ${gaps.size} different gutters (${[...gaps.keys()].join("px, ")}px); there is one: ${[...gaps.values()].join(" | ")}`);
}
/* Every required page has at least one multi-card row at each width of 768 and
 * up (390 is one column by design). Fewer than that means the measurement
 * stopped seeing the cards. */
const MIN_ROWS = Object.keys(REQUIRED).length * WIDTHS.filter((w) => w >= 768).length;
if (rowsMeasured < MIN_ROWS) {
  fail.push(`only ${rowsMeasured} multi-card rows measured across the site, expected at least ${MIN_ROWS} -- the check is not looking`);
}

if (fail.length) {
  console.error(`card layout: ${fail.length} problem(s)\n  ${fail.join("\n  ")}`);
  process.exit(1);
}
console.log(`card layout passed (${pages.length} pages x ${WIDTHS.length} widths, ${rowsMeasured} rows measured)`);
