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
 *      edges, within 1px. The column is the content box of the nearest
 *      `[data-column]` (rail layouts), else the `.wrap` content box, or the
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

/* Every page the alignment work converted, and EXACTLY how many card grids it
 * has, checked at every width. This list is deliberately independent of the
 * class it counts: discovery below reads `.card-grid` from the HTML, so a page
 * that LOST its grid would otherwise drop out of the run and pass, and an
 * exact count stops a grid added in one place from hiding one lost in
 * another. Change a number alongside the markup change that adds or removes a
 * grid. */
const REQUIRED = {
  // 0 is enforced (`!== undefined`): "/" has no .card-grid, and a page that gains one must say so here.
  "/": 0,
  "/features": 0,
  "/comparison": 3,
  "/download": 1,
  "/multistream": 1,
  "/how-to-multistream-from-obs": 1,
  "/free-restream-service": 1,
  "/vs/aitum-multistream": 3,
  "/vs/obs-multi-rtmp": 2,
  "/vs/restreamer": 3,
  "/vs/streamelements": 3,
  "/vs/streamlabs": 1,
  "/docs/install": 1,
  "/docs/tls": 1,
  "/docs/platforms": 1,
  "/docs/configuration": 1,
  "/docs/connecting-platforms": 1,
};

/* How many cards the FIRST row of each variant holds at a viewport width --
 * the breakpoints in global.css. Without this a grid that collapsed to one
 * column passes everything: one card per row has no neighbour to differ from. */
const COLUMNS = {
  "card-grid--2": (w) => (w >= 768 ? 2 : 1),
  "card-grid--3": (w) => (w >= 1024 ? 3 : 1),
  "card-grid--5": (w) => (w >= 1024 ? 3 : w >= 768 ? 2 : 1),
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
    // A rail layout names its main column; tables and grids are held to its content box.
    const column = grid.closest("[data-column]");
    if (column) {
      const b = column.getBoundingClientRect();
      const cs = getComputedStyle(column);
      return {
        left: b.left + px(cs.paddingLeft) + px(cs.borderLeftWidth),
        right: b.right - px(cs.paddingRight) - px(cs.borderRightWidth),
        host: "column",
      };
    }
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
        // data-foot: "required" (every card has one), "optional" (some do), or absent (none may).
        foot: g.dataset.foot ?? "none",
        // data-pager: the docs previous/next pair, which may hold one card at either side.
        pager: g.hasAttribute("data-pager"),
        inDocColumn: !!g.closest("[data-doc-column]"),
        spanFull: [...g.children].some((c) => c.classList.contains("card-span-full")),
        variant: [...g.classList].find((c) => /^card-grid--[0-9]$/.test(c)) ?? null,
        rowsVariant: g.classList.contains("card-grid--rows"),
        left,
        right,
        column: columnOf(g),
        columnGap: px(cs.columnGap),
        items: items.map((c) => {
          const r = c.getBoundingClientRect();
          const feet = [...c.querySelectorAll(".card-foot")].filter((a) => a.getClientRects().length > 0);
          return {
            top: r.top,
            left: r.left,
            right: r.right,
            height: r.height,
            feet: feet.length,
            children: c.children.length,
            cta: feet.length ? Math.max(...feet.map((a) => a.getBoundingClientRect().bottom)) : null,
            footTop: feet.length ? Math.min(...feet.map((a) => a.getBoundingClientRect().top)) : null,
          };
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
      if (REQUIRED[route] !== undefined && cardGrids !== REQUIRED[route]) {
        fail.push(`${at}: ${cardGrids} .card-grid found, expected ${REQUIRED[route]} -- update REQUIRED with the markup change`);
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
        if (!g.variant || !COLUMNS[g.variant]) {
          fail.push(`${name}: .card-grid without a column variant (--2, --3 or --5)`);
          continue;
        }
        /* The variant is chosen from the count, so the count must fit it:
           --2 an even count or an odd one closed by a .card-span-full card,
           --3 a multiple of three, --5 five. Without this, two cards in --3
           pass. The docs pager is the one --2 allowed a single card. */
        const n = g.items.length;
        const fits = {
          "card-grid--2": () => (n >= 2 && (n % 2 === 0 || g.spanFull)) || (g.pager && n >= 1 && n <= 2),
          "card-grid--3": () => n >= 3 && n % 3 === 0,
          "card-grid--5": () => n === 5,
        };
        if (!fits[g.variant]()) {
          fail.push(`${name}: ${n} card(s) in ${g.variant} -- pick the variant for this count`);
        }
        /* The foot is declared, not inferred. A grid whose cards end on a
           recurring line says data-foot="required" and every card must carry
           exactly one; "optional" allows at most one; no attribute allows
           none. Inferring it let every foot be deleted and still pass. */
        if (!["required", "optional", "none"].includes(g.foot)) {
          fail.push(`${name}: data-foot="${g.foot}" -- use "required" or "optional", or leave it off`);
        }
        /* The pager exemption is for one grid: the docs previous/next pair.
           Anywhere else, or as anything but --2, data-pager is a way round
           the row rules, so it fails. */
        if (g.pager && (!g.inDocColumn || g.variant !== "card-grid--2")) {
          fail.push(`${name}: data-pager is only for the --2 previous/next grid on a documentation page`);
        }
        const footed = g.items.filter((c) => c.feet > 0).length;
        if (g.foot === "required" && footed !== n) {
          fail.push(`${name}: data-foot="required" but ${footed} of ${n} cards have a .card-foot`);
        } else if (g.foot === "none" && footed > 0) {
          fail.push(`${name}: ${footed} card(s) have a .card-foot but the grid declares no data-foot`);
        }
        if (g.items.some((c) => c.feet > 1)) fail.push(`${name}: a card has more than one .card-foot`);

        const rows = [];
        for (const it of g.items) {
          const row = rows.find((r) => Math.abs(r[0].top - it.top) <= TOLERANCE);
          if (row) row.push(it);
          else rows.push([it]);
        }
        const want = Math.min(COLUMNS[g.variant](width), g.items.length);
        if (rows.length && rows[0].length !== want) {
          fail.push(`${name}: first row holds ${rows[0].length} card(s), ${g.variant} at ${width}px should hold ${want}`);
        }
        rows.forEach((row, i) => {
          row.sort((a, b) => a.left - b.left);
          if (row.length > 1) rowsMeasured++;
          const hs = row.map((c) => c.height);
          if (Math.max(...hs) - Math.min(...hs) > TOLERANCE) {
            fail.push(`${name} row ${i + 1}: card heights differ (${hs.map((h) => h.toFixed(0)).join(" / ")})`);
          }
          /* --rows promises more: every foot in the row STARTS on one line
             (its rule or first line), not just ends on one. */
          if (g.rowsVariant) {
            const tops = row.map((c) => c.footTop).filter((c) => c != null);
            if (tops.length > 1 && Math.max(...tops) - Math.min(...tops) > TOLERANCE) {
              fail.push(`${name} row ${i + 1}: --rows feet start ${(Math.max(...tops) - Math.min(...tops)).toFixed(1)}px apart`);
            }
            if (row.some((c) => c.children !== 2)) fail.push(`${name} row ${i + 1}: a --rows card must have exactly two children (body, foot)`);
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
        /* Every row spans the grid, left edge to right edge: a short last
           row is an orphan, and a short first row is a variant with more
           columns than cards. The docs pager holding ONE card is exempt -- on
           the first and last documents it sits at the side it points to. */
        if (!(g.pager && n === 1)) {
          rows.forEach((row, i) => {
            const shortL = Math.min(...row.map((c) => c.left)) - g.left;
            const shortR = g.right - Math.max(...row.map((c) => c.right));
            if (shortL > TOLERANCE || shortR > TOLERANCE) {
              const what = i === rows.length - 1 && rows.length > 1 ? "an orphan" : "the row does not fill its columns";
              fail.push(`${name} row ${i + 1}: ${row.length} card(s) leave ${shortL.toFixed(0)}px / ${shortR.toFixed(0)}px of the grid empty (left / right) -- ${what}`);
            }
          });
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
/* A floor of one multi-card row per required page per width from 768 up (390
 * is one column by design). The site measures far more; fewer than this means
 * the measurement stopped seeing the cards. */
const MIN_ROWS = Object.keys(REQUIRED).length * WIDTHS.filter((w) => w >= 768).length;
if (rowsMeasured < MIN_ROWS) {
  fail.push(`only ${rowsMeasured} multi-card rows measured across the site, expected at least ${MIN_ROWS} -- the check is not looking`);
}

if (fail.length) {
  console.error(`card layout: ${fail.length} problem(s)\n  ${fail.join("\n  ")}`);
  process.exit(1);
}
console.log(`card layout passed (${pages.length} pages x ${WIDTHS.length} widths, ${rowsMeasured} rows measured)`);
