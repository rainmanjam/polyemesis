#!/usr/bin/env node
/* The rebuilt inner pages, measured in a real browser against the BUILT site.
 *
 * check-build.mjs reads dist/ as text and the unit tests run in jsdom, so
 * neither can prove what the inner pages promise: that the page is complete
 * with scripts off, that the failover demo is still under reduced motion, that
 * a section link lands on the section, that nothing sideways-scrolls on a
 * phone, that article lines are a readable length, or that a Mermaid diagram
 * renders under the site's real content security policy. Those are layout and
 * behaviour, so this loads the pages in Chromium.
 *
 * CONTEXTS. 1440x900 and 390x844 (a touch phone), each three ways: scripts on,
 * scripts off, and scripts on with prefers-reduced-motion. Every response
 * carries the Content-Security-Policy read from dist/_headers, so a policy
 * violation is a real failure here, as it would be on the site.
 *
 *   console        no console error, uncaught exception or securitypolicyviolation
 *                  during a scroll from the top of the page to the bottom
 *   overflow       the page is no wider than the viewport
 *   index          the in-page index shows its links (a list with scripts off, a
 *                  disclosure on a phone docs page); /404 and /calculator have no
 *                  index at all, no empty rail, and their column fills the content box
 *   small-text     at 390, scripts on and off, no text in <main> renders under 12px
 *                  (SVG text at its on-screen scale; only <sup> and <sub> are skipped)
 *   install-no-js  scripts off: the four install commands sit in visible panels
 *                  and the tab list is not shown
 *   install-tabs   scripts on: one panel at a time, arrows move through the tabs
 *   chooser-no-js  scripts off: every product column, no chooser (/comparison, /vs/restreamer)
 *   chooser-js     scripts on: unchecking a product hides its column in every product
 *                  table and the status line says so
 *   failover-no-js scripts off: #failover shows the live state, no controls
 *   failover-still reduced motion: a MutationObserver sees no change of the demo's
 *                  data-state or log for 4.5 s at rest, and again for 4.5 s after a
 *                  manual step; two pictures 4.5 s apart are identical; it steps by hand
 *   landing        a page opened at #section lands on it: clear of whatever is stuck at
 *                  the top (the site header, the index strip), and within 200px of it,
 *                  with scripts on and off
 *   mermaid        scripts on: every diagram renders to an svg under the policy
 *   mermaid-no-js  scripts off: the diagram's source is shown in its reserved room
 *   doc-index      scrolling a docs page moves aria-current through "On this page"
 *   measure        paragraph lines are 62-80 characters at 1440 (the longest direct
 *                  child paragraph of article.doc-prose, and of a long-form page's
 *                  sections), and doc body text is 17px
 *   task-list      a docs task list stays inside the phone's width
 *
 * NO TIMING. Every wait is on a condition with a long timeout. The fixed waits
 * are windows in which an ABSENCE is asserted (nothing moves for a second) or
 * the two-second settle the page itself holds a section for; they can only make
 * a pass more likely to be a true one.
 *
 * GREEN OVER NOTHING. Every assertion reads an element it first requires, and a
 * missing element is a failure of its own. The run fails if any check did not
 * run in a context it is meant to run in.
 *
 * Needs a Chromium: `npx playwright install chromium` once. Run after
 * `npm run build`. CHECK_INNER_DIST points at another dist/. CHECK_INNER_ONLY=<ids>
 * (local only, refused when CI is set) runs a subset; CHECK_INNER_DEBUG=1 prints stacks.
 */
import { createServer } from "node:http";
import { createReadStream, existsSync, readFileSync, statSync } from "node:fs";
import { extname, join, resolve, sep } from "node:path";
import { availableParallelism } from "node:os";
import { chromium } from "playwright";
import { BUILD_FROM_CLONE, COMPOSE_UP, DOCKER_RUN, INSTALL_SCRIPT } from "../src/data/install.mjs";

const DIST = resolve(process.env.CHECK_INNER_DIST ?? new URL("../dist/", import.meta.url).pathname);
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
  ".json": "application/json",
  ".ico": "image/x-icon",
};
// The policy the site is served with: the first Content-Security-Policy line
// of dist/_headers. Read, not copied, so the check cannot drift from the site.
const CSP_LINES = [...readFileSync(join(DIST, "_headers"), "utf8").matchAll(/^\s*Content-Security-Policy:\s*(.+?)\s*$/gm)];
if (CSP_LINES.length !== 1) throw new Error(`dist/_headers must have exactly one Content-Security-Policy line (the check serves the first one), found ${CSP_LINES.length}`);
const CSP = CSP_LINES[0][1];

const LONG = 60_000; // one wait, never a tuned delay
const MIN_TEXT_PX = 12;
const SETTLE_MS = 2500; // the page holds a section for 2 s while its layout settles (inner.ts)
const MEASURE = { min: 62, max: 80 };

/* Every route, and the contexts it is measured in. A route without `in` runs in all six contexts. The extra routes
 * run only on a phone, scripts on and off (the two states that change what is on screen at 390: a stacked tab panel,
 * a closed disclosure, a hidden chooser), because the wall-clock budget is the sum of every visit. check-layout.mjs
 * already measures their sideways scroll at four widths. The one long-form route measured for line length also runs
 * at 1440 with scripts on. */
const PHONE_ONLY = ["390 js", "390 no-js"];
const ROUTE_SPECS = [
  { path: "/download" },
  { path: "/features" },
  { path: "/comparison" },
  { path: "/vs/restreamer" },
  { path: "/docs/install" },
  { path: "/docs/tls" },
  { path: "/docs/platforms" },
  { path: "/docs/configuration" },
  { path: "/docs/connecting-platforms" },
  { path: "/docs/meta-app-review" },
  { path: "/docs/audio-routing" },
  { path: "/docs/quickstart" },
  { path: "/docs/api" },
  { path: "/404" },
  { path: "/docs", in: PHONE_ONLY },
  { path: "/calculator", in: PHONE_ONLY },
  { path: "/multistream", in: PHONE_ONLY },
  { path: "/how-to-multistream-from-obs", in: PHONE_ONLY },
  { path: "/free-restream-service", in: [...PHONE_ONLY, "1440 js"] },
  { path: "/twitch-vod-track", in: PHONE_ONLY },
  { path: "/vs/aitum-multistream", in: PHONE_ONLY },
  { path: "/vs/obs-multi-rtmp", in: PHONE_ONLY },
  { path: "/vs/streamelements", in: PHONE_ONLY },
  { path: "/vs/streamlabs", in: PHONE_ONLY },
];
const ALL_ROUTES = ROUTE_SPECS.map((r) => r.path);
// CHECK_INNER_ROUTES=/download,/404 narrows a LOCAL run to those routes (the proofs that a check can fail use it).
const ROUTES = process.env.CHECK_INNER_ROUTES ? process.env.CHECK_INNER_ROUTES.split(",").map((x) => x.trim()) : ALL_ROUTES;
/** The routes visited in one context. */
const routesFor = (c) => ROUTES.filter((r) => (ROUTE_SPECS.find((s) => s.path === r)?.in ?? [c.name]).includes(c.name));
// Pages with no headings to index: no rail, and the column fills the content box.
const NO_INDEX_ROUTES = ["/404", "/calculator"];
// A page whose comparison chooser is tested, with the number of product-column tables it drives (/vs/restreamer also
// has a pricing table, which has no product columns and is not part of the chooser).
const CHOOSER_TABLES = { "/comparison": 2, "/vs/restreamer": 1 };
const CHOOSER_ROUTES = Object.keys(CHOOSER_TABLES);
// A long-form page whose paragraph measure is tested at 1440.
const LONGFORM_ROUTE = "/free-restream-service";
const MERMAID_ROUTES = ["/docs/install", "/docs/tls"];
const LANDINGS = [
  ["/download", "need"],
  ["/comparison", "mixes"],
  ["/features", "failover"],
];

const CONTEXTS = [
  { name: "1440 js", width: 1440, height: 900, js: true, reduced: false },
  { name: "390 js", width: 390, height: 844, js: true, reduced: false, phone: true },
  { name: "1440 no-js", width: 1440, height: 900, js: false, reduced: false },
  { name: "390 no-js", width: 390, height: 844, js: false, reduced: false, phone: true },
  { name: "1440 reduced", width: 1440, height: 900, js: true, reduced: true },
  { name: "390 reduced", width: 390, height: 844, js: true, reduced: true, phone: true },
];

/** The file under DIST that a URL path is served from: undefined if none, null if the path leaves DIST. */
function distFile(path) {
  const base = resolve(DIST, "." + path);
  if (base !== DIST && !base.startsWith(DIST + sep)) return null;
  return [base, `${base}.html`, join(base, "index.html")].find((f) => existsSync(f) && statSync(f).isFile());
}

function serve() {
  return new Promise((done) => {
    const srv = createServer((req, res) => {
      const file = distFile(decodeURIComponent(new URL(req.url ?? "/", "http://x").pathname));
      if (file === null) {
        res.writeHead(403).end();
        return;
      }
      if (!file) {
        res.writeHead(404).end();
        return;
      }
      res.writeHead(200, { "content-type": TYPES[extname(file)] ?? "application/octet-stream", "content-security-policy": CSP });
      createReadStream(file).pipe(res);
    });
    srv.listen(0, "127.0.0.1", () => done(srv));
  });
}

/* ---------------------------------------------------------------- reporting */

const fail = [];
const T00 = Date.now();
const at = () => `${((Date.now() - T00) / 1000).toFixed(0).padStart(3)}s`;
/** Record a failure AND print it now: a run killed by the step timeout still shows what had failed. */
const bad = (id, where, msg) => {
  const line = `${id} [${where}] ${msg}`;
  fail.push(line);
  console.error(`${at()} FAIL ${line.length > 700 ? line.slice(0, 700) + "..." : line}`);
};

const DEADLINE_S = Number(process.env.CHECK_INNER_DEADLINE_S ?? 780);
let stopped = false;
const running = new Map();
let runId = 0;
const ran = new Set();

const JS = (c) => c.js;
const phone = (c) => c.phone === true;

/* Every check: where it runs. The table is the single source for what runs and for what the run must
 * have covered, so a renamed route, a skipped branch or a mistyped CHECK_INNER_ONLY cannot end in a
 * green "passed". `run` receives the loaded page. */
const CHECKS = [
  { id: "console", where: () => true, run: async () => {} }, // listeners are on from the start; the scroll is in visit()
  { id: "overflow", where: () => true, run: checkOverflow },
  { id: "index", where: () => true, run: checkIndex },
  { id: "small-text", where: (c) => !c.reduced && phone(c), run: checkSmallText },
  { id: "install-no-js", where: (c, r) => !c.js && r === "/download", run: checkInstallNoJs },
  { id: "install-tabs", where: (c, r) => JS(c) && r === "/download", run: checkInstallTabs },
  { id: "chooser-no-js", where: (c, r) => !c.js && CHOOSER_ROUTES.includes(r), run: checkChooserNoJs },
  { id: "chooser-js", where: (c, r) => JS(c) && CHOOSER_ROUTES.includes(r), run: checkChooserJs },
  { id: "failover-no-js", where: (c, r) => !c.js && r === "/features", run: checkFailoverNoJs },
  { id: "failover-still", where: (c, r) => c.reduced && r === "/features", run: checkFailoverStill },
  { id: "landing", where: (c, r) => !c.reduced && LANDINGS.some(([p]) => p === r), run: checkLanding },
  { id: "mermaid", where: (c, r) => JS(c) && MERMAID_ROUTES.includes(r), run: checkMermaid },
  { id: "mermaid-no-js", where: (c, r) => !c.js && MERMAID_ROUTES.includes(r), run: checkMermaidNoJs },
  { id: "doc-index", where: (c, r) => c.js && !c.reduced && !phone(c) && r === "/docs/audio-routing", run: checkDocIndex },
  { id: "measure", where: (c, r) => c.js && !c.reduced && !phone(c) && (r === "/docs/audio-routing" || r === LONGFORM_ROUTE), run: checkMeasure },
  { id: "task-list", where: (c, r) => c.js && !c.reduced && r === "/docs/meta-app-review", run: checkTaskList },
];
const ONLY = process.env.CHECK_INNER_ONLY ? process.env.CHECK_INNER_ONLY.split(",").map((x) => x.trim()) : null;
if (ONLY) {
  if (process.env.CI) {
    console.error("CHECK_INNER_ONLY is set in CI: a filtered run must not gate a publish. Unset it.");
    process.exit(2);
  }
  const unknown = ONLY.filter((id) => !CHECKS.some((k) => k.id === id));
  if (unknown.length) {
    console.error(`CHECK_INNER_ONLY names unknown check id(s): ${unknown.join(", ")}. Known: ${CHECKS.map((k) => k.id).join(", ")}`);
    process.exit(2);
  }
}
if (process.env.CHECK_INNER_ROUTES) {
  if (process.env.CI) {
    console.error("CHECK_INNER_ROUTES is set in CI: a filtered run must not gate a publish. Unset it.");
    process.exit(2);
  }
  const unknown = ROUTES.filter((r) => !ALL_ROUTES.includes(r));
  if (unknown.length) {
    console.error(`CHECK_INNER_ROUTES names unknown route(s): ${unknown.join(", ")}. Known: ${ALL_ROUTES.join(", ")}`);
    process.exit(2);
  }
}
const wanted = (id) => !ONLY || ONLY.includes(id);
const EXPECTED = CHECKS.filter((k) => wanted(k.id)).flatMap((k) => CONTEXTS.flatMap((c) => routesFor(c).filter((r) => k.where(c, r)).map((r) => `${k.id}|${c.name} ${r}`)));

/** Run one named check; a throw (a missing element, a timeout) is a failure of that check. */
async function check(k, ctx) {
  if (!wanted(k.id) || stopped) return;
  const where = `${ctx.c.name} ${ctx.route}`;
  ran.add(`${k.id}|${where}`);
  const key = ++runId;
  running.set(key, `${k.id} [${where}]`);
  try {
    await k.run(ctx);
  } catch (e) {
    bad(k.id, where, `threw: ${String(e?.message ?? e).split("\n")[0]}${process.env.CHECK_INNER_DEBUG ? "\n" + e?.stack : ""}`);
  } finally {
    running.delete(key);
  }
}

/* ------------------------------------------------------------------ browser */

/** A page with the console, exception and policy listeners already on; failures are attributed to `where`. */
async function newPage(browserCtx, c, where) {
  const page = await browserCtx.newPage();
  page.setDefaultTimeout(LONG);
  page.on("console", (msg) => {
    if (msg.type() === "error") bad("console", where, `console error: ${msg.text()}`);
  });
  page.on("pageerror", (err) => bad("console", where, `uncaught: ${err.message}`));
  if (c.js) {
    await page.addInitScript(() => {
      window.__csp = [];
      document.addEventListener("securitypolicyviolation", (e) => window.__csp.push(`${e.violatedDirective} ${e.blockedURI || "inline"}`));
    });
  }
  return page;
}

async function newContext(browser, c) {
  return browser.newContext({
    viewport: { width: c.width, height: c.height },
    javaScriptEnabled: c.js,
    reducedMotion: c.reduced ? "reduce" : "no-preference",
    ...(c.phone ? { isMobile: true, hasTouch: true } : {}),
  });
}

/** Load a route and, for a scripted context, wait until the network is quiet. */
async function load(page, url, c) {
  const res = await page.goto(url, { waitUntil: "load" });
  if (!res?.ok()) throw new Error(`HTTP ${res?.status()} for ${url}`);
  if (c.js) await page.waitForLoadState("networkidle");
  await page.evaluate(() => document.fonts.ready);
}

/** Scroll from the top to the bottom in screen-sized steps, so every lazy thing is asked for. Stepped from here, not
 *  inside the page: with scripts off a page has no timers or animation frames to wait on, only a synchronous scroll. */
async function scrollThrough(page, c) {
  // A scripted page has lazy work to trigger at every step; a page without script is only asking for lazy images, and each scroll costs it ~0.4 s.
  const step = Math.floor(c.height * (c.js ? 0.8 : 1.2));
  const height = await page.evaluate(() => document.documentElement.scrollHeight);
  for (let y = 0; y <= height + step; y += step) {
    await page.evaluate((to) => scrollTo(0, to), y);
    if (c.js) await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => r())));
  }
}

/** Visit one route in one context: load, scroll top to bottom (the console check), then run every check that applies. */
async function visit(browser, origin, c, route) {
  if (stopped) return;
  const where = `${c.name} ${route}`;
  const browserCtx = await newContext(browser, c);
  try {
    const page = await newPage(browserCtx, c, where);
    await load(page, origin + route, c);
    await scrollThrough(page, c);
    if (c.js) {
      const csp = await page.evaluate(() => window.__csp);
      if (csp.length) bad("console", where, `content security policy violation(s): ${csp.join("; ")}`);
    }
    // A scripted page that has a diagram finishes it before anything else reads the page.
    if (c.js && MERMAID_ROUTES.includes(route)) await page.waitForFunction(() => !document.querySelector('[data-mermaid="pending"]'));
    await page.evaluate(() => scrollTo(0, 0));
    const ctx = { page, browserCtx, c, route, origin, where };
    for (const k of CHECKS) if (k.where(c, route)) await check(k, ctx);
  } finally {
    await browserCtx.close().catch(() => {});
  }
}

/* ------------------------------------------------------------------- checks */

/* Compared with the width the context was asked for, never with window.innerWidth: a phone's layout viewport grows to
 * fit whatever overflows, so innerWidth "agrees" with the overflow and hides it. */
async function checkOverflow({ page, c, route, where }) {
  const m = await page.evaluate(() => ({ sw: document.documentElement.scrollWidth, bw: document.body.scrollWidth }));
  if (m.sw > c.width || m.bw > c.width) bad("overflow", where, `${route} scrolls sideways: scrollWidth ${m.sw} (body ${m.bw}) > ${c.width}`);
}

async function checkIndex({ page, route, where }) {
  const m = await page.evaluate(() => {
    const vis = (el) => el.checkVisibility();
    const roots = [...document.querySelectorAll("[data-index]")];
    const links = roots.flatMap((r) => [...r.querySelectorAll('a[href^="#"]')]);
    const disclosure = roots.some((r) => {
      const d = r.querySelector("details");
      const s = d?.querySelector("summary");
      return Boolean(d && s && vis(s) && d.querySelectorAll('a[href^="#"]').length >= 2);
    });
    const col = document.querySelector("[data-column]");
    const wrap = col?.closest(".wrap");
    let fill = null;
    if (col && wrap) {
      const cs = getComputedStyle(wrap);
      const box = wrap.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
      fill = { col: col.getBoundingClientRect().width, box };
    }
    return {
      roots: roots.length,
      rails: document.querySelectorAll(".rail").length,
      links: links.length,
      visibleLinks: links.filter(vis).length,
      disclosure,
      fill,
    };
  });
  if (NO_INDEX_ROUTES.includes(route)) {
    if (m.roots || m.rails) bad("index", where, `${route} has no headings to index but renders ${m.roots} [data-index] and ${m.rails} .rail`);
    if (!m.fill) bad("index", where, `${route} has no [data-column] inside a .wrap`);
    else if (m.fill.col < 0.9 * m.fill.box) bad("index", where, `${route}'s column is ${m.fill.col.toFixed(0)}px of a ${m.fill.box.toFixed(0)}px content box (under 90%)`);
    return;
  }
  if (m.roots === 0) bad("index", where, `${route} has no [data-index]`);
  else if (m.links < 2) bad("index", where, `the index has ${m.links} link(s)`);
  else if (m.visibleLinks < 2 && !m.disclosure) bad("index", where, `the index has ${m.links} links but none is shown (visible links ${m.visibleLinks}, no visible disclosure)`);
}

async function checkSmallText({ page, route, where }) {
  const small = await page.evaluate(
    (min) => {
      // A selector-ish name for a report line.
      const name = (el) => {
        const cls = typeof el.className === "string" ? el.className.trim().split(/\s+/).filter(Boolean).slice(0, 2).join(".") : "";
        return el.tagName.toLowerCase() + (el.id ? "#" + el.id : "") + (cls ? "." + cls : "");
      };
      const main = document.querySelector("main");
      if (!main) return { missing: true };
      const out = [];
      let seen = 0;
      const walker = document.createTreeWalker(main, NodeFilter.SHOW_TEXT);
      for (let n = walker.nextNode(); n; n = walker.nextNode()) {
        const text = n.nodeValue.trim();
        const el = n.parentElement;
        if (!text || !el || el.closest("sup, sub, script, style, noscript") || !el.checkVisibility()) continue;
        const r = el.getBoundingClientRect();
        if (r.width <= 1 || r.height <= 1) continue; // visually hidden text (.sr-only) is not on screen
        let px = parseFloat(getComputedStyle(el).fontSize);
        const svg = el.closest("svg");
        if (svg) {
          const vb = svg.viewBox?.baseVal;
          const scale = vb && vb.width ? svg.getBoundingClientRect().width / vb.width : 1;
          px *= scale;
        }
        seen++;
        if (px < min - 0.01) out.push(`${name(el)} "${text.slice(0, 40)}" ${px.toFixed(1)}px`);
      }
      return { out, seen };
    },
    MIN_TEXT_PX,
  );
  if (small.missing) return bad("small-text", where, `${route} has no <main>`);
  if (small.seen < 5) bad("small-text", where, `only ${small.seen} text nodes were measured: the walk found almost nothing`);
  const uniq = [...new Set(small.out)];
  if (uniq.length) bad("small-text", where, `text under ${MIN_TEXT_PX}px at 390: ${uniq.slice(0, 8).join("; ")}${uniq.length > 8 ? ` (+${uniq.length - 8} more)` : ""}`);
}

const squash = (s) => s.replace(/\s+/g, " ").trim();
const INSTALL_NEEDLES = [
  ["Docker", squash(DOCKER_RUN.split("\n")[0])],
  ["Compose", squash(COMPOSE_UP)],
  ["script", squash(INSTALL_SCRIPT.split("\n")[0].split(" -fsSLO")[0])], // "curl --proto '=https' ..."
  ["build", "make build"],
];

async function checkInstallNoJs({ page, where }) {
  const m = await page.evaluate(() => {
    const panels = [...document.querySelectorAll('[role="tabpanel"]')];
    return {
      panels: panels.length,
      shown: panels.filter((p) => p.checkVisibility()).map((p) => p.innerText.replace(/\s+/g, " ")),
      lists: [...document.querySelectorAll('[role="tablist"]')].map((t) => t.checkVisibility()),
    };
  });
  if (m.panels !== 4) bad("install-no-js", where, `expected 4 install panels, found ${m.panels}`);
  if (m.shown.length !== 4) bad("install-no-js", where, `${m.shown.length} of ${m.panels} install panels are visible without scripts`);
  if (m.lists.length === 0) bad("install-no-js", where, "no [role=tablist] to test");
  if (m.lists.some(Boolean)) bad("install-no-js", where, "the tab list is shown without scripts: its buttons do nothing");
  for (const [label, needle] of INSTALL_NEEDLES) {
    if (!m.shown.some((t) => t.includes(needle))) bad("install-no-js", where, `no visible panel holds the ${label} command: "${needle}"`);
  }
}

async function checkInstallTabs({ page, where }) {
  const state = () =>
    page.evaluate(() => {
      const tabs = [...document.querySelectorAll('[role="tab"]')];
      const shown = [...document.querySelectorAll('[role="tabpanel"]')].filter((p) => p.checkVisibility());
      return {
        n: tabs.length,
        selected: tabs.findIndex((t) => t.getAttribute("aria-selected") === "true"),
        focused: tabs.findIndex((t) => t === document.activeElement),
        shown: shown.length,
        shownBy: shown[0]?.getAttribute("aria-labelledby") ?? null,
        tabId: tabs.map((t) => t.id),
        listVisible: document.querySelector('[role="tablist"]')?.checkVisibility() ?? false,
      };
    });
  const first = await state();
  if (first.n !== 4) return bad("install-tabs", where, `expected 4 install tabs, found ${first.n}`);
  if (!first.listVisible) return bad("install-tabs", where, "the tab list is not shown with scripts on");
  if (first.shown !== 1) bad("install-tabs", where, `${first.shown} panels are visible at start, expected 1`);
  await page.locator('[role="tab"]').first().focus();
  const steps = [
    ["ArrowRight", 1],
    ["ArrowRight", 2],
    ["ArrowRight", 3],
    ["ArrowLeft", 2],
  ];
  for (const [key, want] of steps) {
    await page.keyboard.press(key);
    await page.waitForFunction((i) => document.querySelectorAll('[role="tab"]')[i]?.getAttribute("aria-selected") === "true", want, { timeout: 5000 }).catch(() => {}); // a key that does nothing is reported below, not waited on for a minute
    const s = await state();
    if (s.selected !== want) bad("install-tabs", where, `after ${key} the selected tab is ${s.selected}, expected ${want}`);
    else if (s.shown !== 1) bad("install-tabs", where, `after ${key} ${s.shown} panels are visible, expected 1`);
    else if (s.shownBy !== s.tabId[want]) bad("install-tabs", where, `after ${key} the visible panel is labelled by ${s.shownBy}, not the selected tab ${s.tabId[want]}`);
    if (s.focused !== want) bad("install-tabs", where, `after ${key} focus is on tab ${s.focused}, expected ${want}`);
  }
}

async function checkChooserNoJs({ page, route, where }) {
  const m = await page.evaluate(() => {
    const tables = [...document.querySelectorAll("table.cmp")].filter((t) => t.querySelector('thead th[data-col="polyemesis"]'));
    return {
      chooser: [...document.querySelectorAll("fieldset[data-chooser]")].map((f) => f.checkVisibility()),
      tables: tables.map((t) => [...t.querySelectorAll("thead th[data-col]")].filter((th) => th.checkVisibility()).map((th) => th.dataset.col)),
    };
  });
  if (m.chooser.length === 0) bad("chooser-no-js", where, "no fieldset[data-chooser] to test");
  if (m.chooser.some(Boolean)) bad("chooser-no-js", where, "the chooser is shown without scripts");
  if (m.tables.length !== CHOOSER_TABLES[route]) bad("chooser-no-js", where, `expected ${CHOOSER_TABLES[route]} product comparison table(s), found ${m.tables.length}`);
  m.tables.forEach((cols, i) => {
    if (cols.length !== 3) bad("chooser-no-js", where, `table ${i + 1} shows ${cols.length} product columns (${cols.join(", ")}), expected 3`);
  });
}

async function checkChooserJs({ page, route, where }) {
  const box = page.locator('fieldset[data-chooser] input[data-chooser-col="restream-io"]');
  await page.locator("fieldset[data-chooser]").waitFor({ state: "visible" });
  await box.uncheck();
  const read = () =>
    page.evaluate(() => {
      const cols = [...document.querySelectorAll('[data-col="restream-io"]')];
      const tables = [...document.querySelectorAll("table.cmp")].filter((t) => t.querySelector('thead th[data-col="polyemesis"]'));
      return {
        cols: cols.length,
        hiddenAttr: cols.filter((e) => e.hidden).length,
        shown: cols.filter((e) => e.checkVisibility()).length,
        tablesWith: tables.filter((t) => t.querySelector('[data-col="restream-io"]')).length,
        tables: tables.length,
        status: document.querySelector('fieldset[data-chooser] [role="status"]')?.textContent.trim() ?? null,
      };
    });
  const off = await read();
  if (off.tables !== CHOOSER_TABLES[route] || off.tablesWith !== off.tables) bad("chooser-js", where, `the restream.io column is in ${off.tablesWith} of ${off.tables} tables`);
  if (off.cols === 0 || off.hiddenAttr !== off.cols) bad("chooser-js", where, `unchecking restream.io set hidden on ${off.hiddenAttr} of ${off.cols} of its cells`);
  if (off.shown !== 0) bad("chooser-js", where, `${off.shown} restream.io cells are still visible`);
  if (off.status !== "Showing polyemesis and Restreamer") bad("chooser-js", where, `status line reads ${JSON.stringify(off.status)}, expected "Showing polyemesis and Restreamer"`);
  await box.check();
  const on = await read();
  if (on.hiddenAttr !== 0 || on.shown !== on.cols) bad("chooser-js", where, `checking restream.io again left ${on.hiddenAttr} cells hidden`);
}

async function checkFailoverNoJs({ page, where }) {
  const m = await page.evaluate(() => {
    const root = document.querySelector("#failover");
    const frame = root?.querySelector("[data-failover] [data-frame]");
    if (!root || !frame) return { missing: !root ? "#failover" : "[data-frame]" };
    const text = (el) => (el?.checkVisibility() ? el.textContent.trim() : null);
    return {
      state: frame.dataset.state,
      frameShown: frame.checkVisibility(),
      controls: [...root.querySelectorAll("[data-fo-controls]")].map((e) => e.checkVisibility()),
      enc: text(frame.querySelector("[data-enc]")),
      dests: [...frame.querySelectorAll("[data-dest]")].map(text),
      slate: text(frame.querySelector('[data-s="slate"]')),
      ready: text(frame.querySelector('[data-s="live drop recovered"]')),
    };
  });
  if (m.missing) return bad("failover-no-js", where, `${m.missing} is not on /features`);
  if (m.state !== "live" || !m.frameShown) bad("failover-no-js", where, `the demo's resting state is ${JSON.stringify(m.state)}, shown ${m.frameShown}; expected "live", shown`);
  if (m.controls.length === 0 || m.controls.some(Boolean)) bad("failover-no-js", where, "the demo's controls are shown without scripts");
  if (m.enc !== "Encoder connected") bad("failover-no-js", where, `encoder line reads ${JSON.stringify(m.enc)}`);
  if (m.dests.length !== 3 || m.dests.some((d) => d !== "live")) bad("failover-no-js", where, `destinations read ${JSON.stringify(m.dests)}, expected three "live"`);
  if (m.slate !== null || m.ready !== "ready") bad("failover-no-js", where, `slate row reads on-air ${JSON.stringify(m.slate)} / ready ${JSON.stringify(m.ready)} in the live state`);
}

/* The longest stage of the demo is 3.4 s and the shortest 1.8 s, so a one-second picture window can sit inside one stage
 * of a demo that is playing. A window longer than the whole cycle's longest stage, watched for every change of the
 * frame's data-state and of its log, cannot. */
const STILL_WINDOW_MS = 4500;

async function checkFailoverStill({ page, where }) {
  const frame = page.locator("#failover [data-failover] [data-frame]");
  await frame.scrollIntoViewIfNeeded();
  await page.locator("#failover [data-fo-controls]").waitFor({ state: "visible" }); // the demo has started and shows its controls
  const gate = await page.evaluate(() => document.documentElement.classList.contains("motion-on"));
  if (gate) bad("failover-still", where, "html.motion-on is set under prefers-reduced-motion");
  // Every change to the frame's data-state, its encoder line, its destinations and its log, with the time it happened.
  await page.evaluate(() => {
    const root = document.querySelector("#failover [data-failover]");
    window.__fo = [];
    const t0 = performance.now();
    const note = (what) => window.__fo.push(`${what} at ${Math.round(performance.now() - t0)}ms`);
    new MutationObserver((rs) => {
      for (const r of rs) {
        if (r.type === "attributes") note(`${r.attributeName} -> ${r.target.getAttribute(r.attributeName)}`);
        else note(r.type === "childList" ? "log or text changed" : "text changed");
      }
    }).observe(root, { attributes: true, attributeFilter: ["data-state"], childList: true, characterData: true, subtree: true });
  });
  const changes = () => page.evaluate(() => window.__fo.splice(0));
  const shot = () => frame.screenshot({ animations: "allow", caret: "initial" });
  const a = await shot();
  await page.waitForTimeout(STILL_WINDOW_MS); // an absence window: nothing may change in it
  const b = await shot();
  const atRest = await changes();
  if (atRest.length) bad("failover-still", where, `the demo changed ${atRest.length} time(s) at rest in ${STILL_WINDOW_MS} ms: ${atRest.slice(0, 4).join("; ")}`);
  if (!a.equals(b)) bad("failover-still", where, `two pictures of the failover demo, ${STILL_WINDOW_MS} ms apart, differ at rest`);
  if ((await frame.getAttribute("data-state")) !== "live") bad("failover-still", where, "the resting state is not live");
  // By hand: stage 3 is "Slate on air" (data-stage 2).
  await page.locator('#failover [data-stage="2"]').click();
  await page.waitForFunction(() => document.querySelector("#failover [data-frame]")?.dataset.state === "slate");
  const stepped = await changes(); // the click's own change, and the window starts after it
  if (stepped.length === 0) bad("failover-still", where, "the observer saw nothing when the step to the slate was made: it is not watching the demo");
  const c = await shot();
  await page.waitForTimeout(STILL_WINDOW_MS);
  const d = await shot();
  const afterStep = await changes();
  if (afterStep.length) bad("failover-still", where, `after a manual step the demo moved on by itself ${afterStep.length} time(s) in ${STILL_WINDOW_MS} ms: ${afterStep.slice(0, 4).join("; ")}`);
  if ((await frame.getAttribute("data-state")) !== "slate") bad("failover-still", where, "after a manual step to the slate, the state is not slate");
  if (!c.equals(d)) bad("failover-still", where, `after a manual step, two pictures of the demo ${STILL_WINDOW_MS} ms apart differ`);
  if (a.equals(c)) bad("failover-still", where, "stepping to the slate changed nothing on screen");
}

/** The bottom edge of whatever is stuck across the top of the screen: the site header, and the index strip where it
 *  shows. Found by what the page computes (sticky or fixed, full width, at the top), not by a class name. */
const STUCK_BOTTOM = () => {
  let bottom = 0;
  for (const el of document.body.querySelectorAll("*")) {
    const pos = getComputedStyle(el).position;
    if (pos !== "sticky" && pos !== "fixed") continue;
    const r = el.getBoundingClientRect();
    if (!el.checkVisibility() || r.width < innerWidth * 0.9 || r.height < 1 || r.top > 120 || r.bottom <= 0) continue;
    bottom = Math.max(bottom, r.bottom);
  }
  return bottom;
};

async function checkLanding({ browserCtx, route, c, origin, where }) {
  const id = LANDINGS.find(([p]) => p === route)[1];
  const page = await newPage(browserCtx, c, where);
  try {
    await load(page, `${origin}${route}#${id}`, c);
    await page.waitForTimeout(SETTLE_MS); // the page holds the section for two seconds while the layout settles
    const m = await page.evaluate((target) => {
      const el = document.getElementById(target);
      return { top: el ? el.getBoundingClientRect().top : null, scrolled: scrollY, strip: Boolean([...document.querySelectorAll(".idx-strip")].find((e) => e.checkVisibility())) };
    }, id);
    m.stuckBottom = await page.evaluate(STUCK_BOTTOM);
    if (m.top === null) return bad("landing", where, `no #${id} on ${route}`);
    if (m.stuckBottom <= 0) return bad("landing", where, `nothing is stuck at the top of ${route} to land clear of (the site header should be)`);
    if (m.scrolled <= 0) return bad("landing", where, `${route}#${id} did not scroll the page at all`);
    if (c.phone && c.js && !m.strip) return bad("landing", where, `no visible .idx-strip on ${route} at 390 to land clear of`);
    if (m.top < m.stuckBottom - 1) bad("landing", where, `${route}#${id} lands at ${m.top.toFixed(0)}px, under what is stuck at the top, which ends at ${m.stuckBottom.toFixed(0)}px`);
    else if (m.top > 200) bad("landing", where, `${route}#${id} lands ${m.top.toFixed(0)}px from the top of the screen, expected clear of the header and within 200px`);
  } finally {
    await page.close();
  }
}

async function checkMermaid({ page, route, where }) {
  const m = await page.evaluate(() => {
    const all = [...document.querySelectorAll("[data-mermaid]")];
    return {
      all: all.length,
      done: all.filter((e) => e.dataset.mermaid === "done" && e.querySelector("svg")).length,
      failed: all.filter((e) => e.dataset.mermaid === "failed").length,
      pending: all.filter((e) => e.dataset.mermaid === "pending").length,
    };
  });
  if (m.all === 0) return bad("mermaid", where, `${route} has no [data-mermaid] diagram`);
  if (m.failed || m.pending || m.done !== m.all) bad("mermaid", where, `${m.all} diagram(s): ${m.done} rendered an svg, ${m.failed} failed, ${m.pending} still pending`);
}

async function checkMermaidNoJs({ page, c, route, where }) {
  const m = await page.evaluate(() =>
    [...document.querySelectorAll('[data-mermaid="pending"]')].map((e) => {
      const r = e.getBoundingClientRect();
      return { shown: e.checkVisibility(), text: e.textContent.trim().length, h: r.height, min: parseFloat(getComputedStyle(e).minHeight) || 0, right: r.right };
    }),
  );
  if (m.length === 0) return bad("mermaid-no-js", where, `${route} has no pending [data-mermaid] diagram`);
  m.forEach((d, i) => {
    if (!d.shown || d.text === 0) bad("mermaid-no-js", where, `diagram ${i + 1} shows no source text without scripts (shown ${d.shown}, ${d.text} characters)`);
    else if (d.min < 100) bad("mermaid-no-js", where, `diagram ${i + 1} reserves ${d.min}px, so the page shifts when the diagram renders`);
    else if (d.h < d.min - 1) bad("mermaid-no-js", where, `diagram ${i + 1} is ${d.h.toFixed(0)}px tall, under its reserved ${d.min}px`);
    if (d.right > c.width + 1) bad("mermaid-no-js", where, `diagram ${i + 1} runs ${(d.right - c.width).toFixed(0)}px past the viewport`);
  });
}

async function checkDocIndex({ page, where }) {
  const ids = await page.evaluate(() => {
    const nav = [...document.querySelectorAll('nav[aria-label="On this page"]')].find((n) => n.checkVisibility());
    return nav ? [...nav.querySelectorAll('a[href^="#"]')].map((a) => a.getAttribute("href").slice(1)) : null;
  });
  if (!ids) return bad("doc-index", where, 'no visible nav[aria-label="On this page"] at 1440');
  if (ids.length < 4) return bad("doc-index", where, `the index lists only ${ids.length} sections`);
  const current = () =>
    page.evaluate(() => {
      const nav = [...document.querySelectorAll('nav[aria-label="On this page"]')].find((n) => n.checkVisibility());
      return [...nav.querySelectorAll('[aria-current="true"]')].map((a) => a.getAttribute("href").slice(1));
    });
  const seen = [];
  for (const id of [ids[1], ids[Math.floor(ids.length / 2)], ids[ids.length - 2]]) {
    await page.evaluate((t) => document.getElementById(t).scrollIntoView({ block: "start", behavior: "instant" }), id);
    const ok = await page
      .waitForFunction(
        (t) => {
          const nav = [...document.querySelectorAll('nav[aria-label="On this page"]')].find((n) => n.checkVisibility());
          return nav?.querySelector('[aria-current="true"]')?.getAttribute("href") === `#${t}`;
        },
        id,
        { timeout: 10_000 },
      )
      .then(() => true, () => false);
    const now = await current();
    if (!ok) bad("doc-index", where, `scrolled to #${id}, but aria-current marks ${JSON.stringify(now)}`);
    seen.push(...now);
  }
  if (new Set(seen).size < 3) bad("doc-index", where, `aria-current did not move: it only ever marked ${JSON.stringify([...new Set(seen)])}`);
}

async function checkMeasure({ page, route, where }) {
  const sel = route === LONGFORM_ROUTE ? ".longform > section > p" : "article.doc-prose > p";
  const m = await page.evaluate((sel) => {
    // Direct children only: a paragraph inside a list, table or callout has its own, narrower measure.
    const paras = [...document.querySelectorAll(sel)].filter((p) => p.checkVisibility());
    if (!paras.length) return null;
    const p = paras.reduce((a, b) => (b.textContent.length > a.textContent.length ? b : a));
    const lh = parseFloat(getComputedStyle(p).lineHeight) || parseFloat(getComputedStyle(p).fontSize) * 1.5;
    const range = document.createRange();
    range.selectNodeContents(p);
    const centres = [...range.getClientRects()].filter((r) => r.width > 0).map((r) => (r.top + r.bottom) / 2).sort((a, b) => a - b);
    let lines = 0;
    let last = -Infinity;
    for (const y of centres) {
      if (y - last > lh / 2) lines++;
      last = y;
    }
    const chars = p.textContent.replace(/\s+/g, " ").trim().length;
    return { chars, lines, avg: chars / Math.max(lines, 1), size: getComputedStyle(p).fontSize, head: p.textContent.trim().slice(0, 40) };
  }, sel);
  if (!m) return bad("measure", where, `no ${sel} on ${route}`);
  if (m.lines < 4) bad("measure", where, `the longest paragraph ("${m.head}") has only ${m.lines} lines: too short to measure`);
  else if (m.avg < MEASURE.min || m.avg > MEASURE.max) bad("measure", where, `the longest paragraph averages ${m.avg.toFixed(1)} characters a line (${m.chars} over ${m.lines} lines), expected ${MEASURE.min} to ${MEASURE.max}`);
  if (m.size !== "17px") bad("measure", where, `article paragraph font-size is ${m.size}, expected 17px`);
}

async function checkTaskList({ page, c, where }) {
  const m = await page.evaluate((width) => {
    const items = [...document.querySelectorAll("li.task-list-item")].filter((e) => e.checkVisibility());
    return { n: items.length, out: items.filter((e) => e.getBoundingClientRect().right > width + 1).length };
  }, c.width);
  if (m.n < 3) bad("task-list", where, `expected a task list of 3 or more items, found ${m.n}`);
  if (m.out) bad("task-list", where, `${m.out} task-list items run past the viewport`);
}

/* --------------------------------------------------------------------- main */

const deadlineTimer = setTimeout(() => {
  stopped = true;
  console.error(`DEADLINE: ${DEADLINE_S}s passed. Still running: ${[...running.values()].join("; ") || "nothing"}.`);
  if (fail.length) console.error(`inner pages: ${new Set(fail).size} problem(s) so far (run STOPPED at the deadline)`);
  process.exit(1);
}, DEADLINE_S * 1000);
deadlineTimer.unref();

const srv = await serve();
const origin = `http://127.0.0.1:${srv.address().port}`;
const browser = await chromium.launch();
try {
  // One unit per (context, route), longest-visit work first being unnecessary: the queue is shared by `width` workers.
  const units = CONTEXTS.flatMap((c) =>
    routesFor(c).map((route) => async () => {
      try {
        await visit(browser, origin, c, route);
      } catch (e) {
        bad("visit", `${c.name} ${route}`, `threw: ${String(e?.message ?? e).split("\n")[0]}${process.env.CHECK_INNER_DEBUG ? "\n" + e?.stack : ""}`);
      }
    }),
  );
  const width = Number(process.env.CHECK_INNER_CONCURRENCY) || Math.min(CONTEXTS.length, Math.max(2, availableParallelism()));
  const queue = [...units];
  console.log(`serving ${DIST} at ${origin}; ${CONTEXTS.length} contexts, ${units.length} visits, ${width} at a time (${availableParallelism()} CPUs)`);
  await Promise.all(Array.from({ length: width }, async () => { while (queue.length && !stopped) await queue.shift()(); }));
} finally {
  // Never hang here: a wedged browser gets ten seconds to close, then the process leaves regardless.
  await Promise.race([browser.close().catch(() => {}), new Promise((r) => setTimeout(r, 10_000))]);
  srv.closeAllConnections?.();
  srv.close();
}

clearTimeout(deadlineTimer);
const missing = EXPECTED.filter((k) => !ran.has(k));
if (missing.length) bad("coverage", "run", `${missing.length} check run(s) did not happen, e.g. ${missing.slice(0, 3).join(", ")}`);
const total = `${((Date.now() - T00) / 1000).toFixed(1)}s`;
if (fail.length) {
  const seen = new Set();
  const uniq = fail.filter((f) => !seen.has(f) && seen.add(f));
  console.error(`inner pages: ${uniq.length} problem(s)\n  ${uniq.join("\n  ")}`);
  process.exit(1);
}
if (ONLY || process.env.CHECK_INNER_ROUTES) console.log(`PARTIAL RUN, NOT A PASS: only ${ONLY?.join(", ") ?? "every check"} on ${ROUTES.join(", ")} ran (CHECK_INNER_ONLY / CHECK_INNER_ROUTES). No failures in what ran. ${total}`);
else console.log(`inner pages passed (${CONTEXTS.length} contexts, ${ran.size} check runs, ${total})`);
