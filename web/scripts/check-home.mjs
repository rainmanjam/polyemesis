#!/usr/bin/env node
/* The home page, measured in a real browser against the BUILT site.
 *
 * check-build.mjs reads dist/ as text and the unit tests run in jsdom, so
 * neither can prove what the homepage depends on: that the headline, the
 * primary action and the install command share the first screen of a phone,
 * that nothing scripts do throws or trips the content security policy, that
 * the page is complete without script, that three.js and the 3D model are only
 * fetched when they are needed, that a paused page is really still, or that a
 * long label does not break a card. Those are layout and behaviour, so this
 * loads / in Chromium.
 *
 * CONTEXTS. 1440x900 and 390x844 (a touch phone), each three ways: script on,
 * script off, and script on with prefers-reduced-motion. The lazy-3D checks add
 * a browser with software WebGL forced on and one with WebGL switched off.
 * Every response carries the Content-Security-Policy read from dist/_headers,
 * so a policy violation is a real failure here, as it would be on the site.
 *
 *   first-screen  h1, the Install button and the first line of the Docker
 *                 command are above the fold, in each of the three examples;
 *                 no sideways scroll
 *   console       no console error, uncaught exception or securitypolicyviolation
 *                 during a scroll that pauses in every section
 *   keyboard      arrows/Home/End move through the case tabs and the install
 *                 tabs, Space toggles a matrix cell, every Stream Deck legend
 *                 control is reached by Tab and operated by Enter
 *   no-script     the h1, the sports panel with five destinations, the Docker
 *                 command, all eleven headings and both install links; no
 *                 html.motion-on
 *   still         under reduced motion, and after Pause, over a 13 s window: no DOM
 *                 mutation or running animation in an animated block, no WebGL or
 *                 2D canvas draw call, every <video> paused and not advanced, no
 *                 running SMIL, no animated image format; each block's pixels
 *                 settle before the window and again after it
 *   lazy-3d       three.js is not requested before boot, the model not at all
 *                 until #deck approaches, then once; with WebGL off both posters
 *                 stay and the console is clean
 *   contrast      the h1 and the hero lede are at least 7:1 on the page ground
 *   stability     layout shift at most 0.1 on a phone, LCP element in [data-hero]
 *   phone-fit     at 390: the long destination names fit their containers, and
 *                 no text renders under 12px (SVG text at its on-screen scale)
 *   length        page height at 390, at most 22,500px
 *   disclosure    phone: each collapsed block is a real closed <details> with a
 *                 summary that works from the keyboard; desktop: the same
 *                 content is shown with no disclosure control
 *   deck-stability the Stream Deck log's full show cycle at 560, 430, 390, 360 and 320 wide: nothing below it moves
 *   matrix-phone  at 390, 360 and 320 the mix matrix is cards: nothing past the viewport, no sideways scroll,
 *                 every track toggle visible, 44px+, named, and working (click, Space, preset)
 *   journey-bar   at 390 and 360 all five stages fit; at 320 the bar scrolls and each active stage is in view
 *
 * NO TIMING. Nothing here depends on a frame rate or on how long a thing takes:
 * every wait is on a condition (a response, an element, a class, a draw-call
 * count that has stopped changing, fonts.ready) with a long timeout, so a slow
 * runner is slow, not wrong. The only fixed sleeps are windows in which an
 * ABSENCE is asserted (nothing requested, nothing changed), and they can only
 * make a pass more likely to be a true one, never a failure flaky.
 *
 * GREEN OVER NOTHING. Every assertion reads an element it first requires, and
 * a missing element is a failure of its own, never a skipped check.
 *
 * Needs a Chromium: `npx playwright install chromium` once. Run after
 * `npm run build`. CHECK_HOME_DIST points at another dist/. Every browser meant to
 * have WebGL is launched with software-WebGL flags (a GPU-less runner's setup).
 * Rehearsal switches: CHECK_HOME_THROTTLE=<n> slows the CPU n times; CHECK_HOME_NO_GL=1
 * removes WebGL (the run must then fail); CHECK_HOME_ONLY=<ids> (local only, refused
 * when CI is set) runs a subset; CHECK_HOME_DEBUG=1 prints stacks.
 */
import { createServer } from "node:http";
import { createReadStream, existsSync, readFileSync, statSync } from "node:fs";
import { extname, join, resolve, sep } from "node:path";
import { availableParallelism } from "node:os";
import { chromium } from "playwright";
import { CASES, CASE_ORDER } from "../src/data/cases.mjs";
import { DOCKER_RUN } from "../src/data/install.mjs";

const DIST = resolve(process.env.CHECK_HOME_DIST ?? new URL("../dist/", import.meta.url).pathname);
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
  ".glb": "model/gltf-binary",
  ".json": "application/json",
  ".ico": "image/x-icon",
};
// The policy the site is served with: the first Content-Security-Policy line
// of dist/_headers. Read, not copied, so the check cannot drift from the site.
const CSP_LINES = [...readFileSync(join(DIST, "_headers"), "utf8").matchAll(/^\s*Content-Security-Policy:\s*(.+?)\s*$/gm)];
if (CSP_LINES.length !== 1) throw new Error(`dist/_headers must have exactly one Content-Security-Policy line (the check serves the first one), found ${CSP_LINES.length}`);
const CSP = CSP_LINES[0][1];

const MIN_CONTRAST = 7;
const MAX_SHIFT = 0.1;
// Guards the tallest example (worship) at 390 wide, with room to spare; secondary content collapses on phones before primary content does (R15). 22,500 since R17, which made room for the matrix's phone cards.
const MAX_HEIGHT_390 = 22500;
const MIN_TEXT_PX = 12;
const LONG = 60_000; // one wait, never a tuned delay (a 2-vCPU runner draws software WebGL slowly)
const GL_ARGS = ["--use-gl=angle", "--use-angle=swiftshader", "--enable-unsafe-swiftshader", "--ignore-gpu-blocklist"];
const NO_GL_ARGS = ["--disable-gpu", "--disable-webgl"];
const MODEL = "/models/stream-deck-plus.glb";

// The section headings, as the page's own ids name them: the hero's h1, nine
// sections' h2, and the video tour's h3 inside the first section.
const HEADINGS = ["[data-hero] h1", "#h-how", "[data-tour] h3", "#h-matrix", "#h-audiences", "#h-features-tour", "#h-deck", "#h-calc", "#h-why", "#h-install", "#h-use"];
const SECTIONS = ["[data-hero]", "#how", "#matrix", "#audiences", "#features-tour", "#deck", "#calc", "#why", "#install", "#use"];
const EXPECT_DESTS = CASES.sports.dests.length;

const allNames = CASE_ORDER.flatMap((k) => CASES[k].dests.map((d) => d.name));
const LONGEST = Math.max(...allNames.map((n) => n.length));
const LONG_NAMES = [...new Set(["YouTube · Português", ...allNames.filter((n) => n.length === LONGEST)])];

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

/* Every check, and every context it must run in. The run fails if any of these did not run, so a
 * renamed id, a skipped branch or a mistyped CHECK_HOME_ONLY cannot end in a green "passed". */
const EXPECTED = {
  "first-screen": ["1440 js", "390 js", "1440 no-js", "390 no-js"],
  "no-script": ["1440 no-js", "390 no-js"],
  contrast: ["1440 js", "390 js"],
  keyboard: ["1440 js", "390 js"],
  length: ["390 js"],
  disclosure: ["1440 js", "390 js"],
  console: ["1440 js", "390 js"],
  still: ["1440 reduced", "390 reduced", "1440 js pause", "390 js pause"],
  "phone-fit": ["390 js"],
  stability: ["390 js"],
  "lazy-3d": ["1440 js", "1440 js no-webgl"],
  "model-fail": ["1440 js model-fail"],
  "deck-stability": ["560 deck", "430 deck", "390 deck", "360 deck", "320 deck"],
  "matrix-phone": ["390 phone", "360 phone", "320 phone"],
  "journey-bar": ["390 phone", "360 phone", "320 phone"],
};
// CHECK_HOME_ONLY=still,lazy-3d narrows a LOCAL run to those checks (the page-defect proofs use it). It is
// refused in CI, and an id that is not in EXPECTED is an error, never a silent skip.
const ONLY = process.env.CHECK_HOME_ONLY ? process.env.CHECK_HOME_ONLY.split(",").map((x) => x.trim()) : null;
if (ONLY) {
  if (process.env.CI) {
    console.error("CHECK_HOME_ONLY is set in CI: a filtered run must not gate a publish. Unset it.");
    process.exit(2);
  }
  const unknown = ONLY.filter((id) => !(id in EXPECTED));
  if (unknown.length) {
    console.error(`CHECK_HOME_ONLY names unknown check id(s): ${unknown.join(", ")}. Known: ${Object.keys(EXPECTED).join(", ")}`);
    process.exit(2);
  }
}
const ran = new Set();

const fail = [];
const log = [];
const T00 = Date.now();
const at = () => `${((Date.now() - T00) / 1000).toFixed(0).padStart(3)}s`;
/** Record a failure AND print it now: a run killed by the step timeout still shows what had failed. */
const bad = (id, ctx, msg) => {
  const line = `${id} [${ctx}] ${msg}`;
  fail.push(line);
  console.error(`${at()} FAIL ${line.length > 600 ? line.slice(0, 600) + "..." : line}`);
};

// A global deadline, under the workflow step's timeout (15 minutes): at DEADLINE_S the run stops starting
// work, prints what it has, says which checks were still running, closes everything and exits non-zero.
const DEADLINE_S = Number(process.env.CHECK_HOME_DEADLINE_S ?? 780);
let stopped = false;
const running = new Map();
let runId = 0;

/** Run one named check; a throw (a missing element, a timeout) is a failure of that check. */
async function check(id, ctx, fn) {
  if (ONLY && !ONLY.includes(id)) return;
  if (stopped) return;
  if (!(id in EXPECTED) || !EXPECTED[id].includes(ctx)) bad("coverage", ctx, `check "${id}" ran in a context that EXPECTED does not list`);
  ran.add(`${id}|${ctx}`);
  const t0 = Date.now();
  const failed0 = fail.length;
  const key = ++runId;
  running.set(key, `${id} [${ctx}]`);
  try {
    await fn();
  } catch (e) {
    bad(id, ctx, `threw: ${String(e?.message ?? e).split("\n")[0]}${process.env.CHECK_HOME_DEBUG ? "\n" + e?.stack : ""}`);
  } finally {
    running.delete(key);
  }
  const secs = ((Date.now() - t0) / 1000).toFixed(1);
  const n = fail.slice(failed0).filter((f) => f.startsWith(id)).length;
  timings.push(`${id} [${ctx}] ${secs}s`);
  console.log(`${at()} done ${id} [${ctx}] ${secs}s${n ? ` - ${n} failure(s)` : ""}`);
}
const timings = [];
const durations = [];

const CONTEXTS = [
  { name: "1440 js", width: 1440, height: 900, js: true, reduced: false },
  { name: "390 js", width: 390, height: 844, js: true, reduced: false, phone: true },
  { name: "1440 no-js", width: 1440, height: 900, js: false, reduced: false },
  { name: "390 no-js", width: 390, height: 844, js: false, reduced: false, phone: true },
  { name: "1440 reduced", width: 1440, height: 900, js: true, reduced: true },
  { name: "390 reduced", width: 390, height: 844, js: true, reduced: true, phone: true },
];

/* Runs in every page before its own scripts: a count of WebGL draw calls (the
 * one thing a still 3D canvas must not do) and the policy violations seen. */
function instrument() {
  window.__draw = 0;
  for (const C of [window.WebGL2RenderingContext, window.WebGLRenderingContext]) {
    if (!C) continue;
    for (const n of ["drawElements", "drawArrays", "drawElementsInstanced", "drawArraysInstanced", "drawRangeElements"]) {
      const orig = C.prototype[n];
      if (!orig) continue;
      C.prototype[n] = function (...a) {
        window.__draw++;
        return orig.apply(this, a);
      };
    }
    // Extension entry points that also draw: WEBGL_multi_draw (multiDraw*WEBGL), ANGLE_instanced_arrays,
    // the base-instance extensions. Their methods are wrapped on the object getExtension hands out.
    const getExt = C.prototype.getExtension;
    C.prototype.getExtension = function (name) {
      const ext = getExt.call(this, name);
      if (ext && !ext.__counted) {
        for (const k of Object.keys(Object.getPrototypeOf(ext))) {
          if (!/^(multiDraw|draw)\w*(WEBGL|ANGLE)$/.test(k) || typeof ext[k] !== "function") continue;
          const orig = ext[k];
          ext[k] = function (...a) {
            window.__draw++;
            return orig.apply(this, a);
          };
        }
        try {
          Object.defineProperty(ext, "__counted", { value: true });
        } catch {}
      }
      return ext;
    };
  }
  // The same count for 2D canvases (the Stream Deck's key faces, strip and glow are drawn on 2D canvases and
  // uploaded as textures): every call that changes a 2D canvas's pixels, tallied per canvas so a failure can say
  // which. The page starts no Worker, so this init script sees every 2D context there is.
  window.__draw2d = 0;
  window.__c2d = new Map();
  const count2d = (cv) => {
    window.__draw2d++;
    window.__c2d.set(cv, (window.__c2d.get(cv) ?? 0) + 1);
  };
  for (const C of [window.CanvasRenderingContext2D, window.OffscreenCanvasRenderingContext2D]) {
    if (!C) continue;
    for (const n of ["clearRect", "fillRect", "strokeRect", "fill", "stroke", "fillText", "strokeText", "drawImage", "putImageData", "drawFocusIfNeeded", "reset"]) {
      const orig = C.prototype[n];
      if (typeof orig !== "function") continue;
      C.prototype[n] = function (...a) {
        count2d(this.canvas);
        return orig.apply(this, a);
      };
    }
  }
  if (window.ImageBitmapRenderingContext) {
    const orig = ImageBitmapRenderingContext.prototype.transferFromImageBitmap;
    ImageBitmapRenderingContext.prototype.transferFromImageBitmap = function (...a) {
      count2d(this.canvas);
      return orig.apply(this, a);
    };
  }
  window.__csp = [];
  document.addEventListener("securitypolicyviolation", (e) => window.__csp.push(`${e.violatedDirective} ${e.blockedURI || "inline"}`));
}

/** A fresh context and page with the console and exception listeners (check "console") already on. */
async function open(browser, c, extra = {}) {
  const ctx = await browser.newContext({
    viewport: { width: c.width, height: c.height },
    javaScriptEnabled: c.js,
    reducedMotion: c.reduced ? "reduce" : "no-preference",
    ...(c.phone ? { isMobile: true, hasTouch: true } : {}),
    ...extra,
  });
  const page = await ctx.newPage();
  page.setDefaultTimeout(LONG);
  page.on("console", (msg) => {
    if (msg.type() === "error") bad("console", c.name, `console error: ${msg.text()}`);
  });
  page.on("pageerror", (err) => bad("console", c.name, `uncaught: ${err.message}`));
  await page.addInitScript(instrument);
  // CHECK_HOME_THROTTLE=4 slows the page's CPU 4x (a way to rehearse a small CI runner).
  if (process.env.CHECK_HOME_THROTTLE) {
    const cdp = await ctx.newCDPSession(page);
    await cdp.send("Emulation.setCPUThrottlingRate", { rate: Number(process.env.CHECK_HOME_THROTTLE) });
  }
  return { ctx, page };
}

/** Load /, and for a scripted context wait until boot has run and every chunk it asked for has arrived. */
async function load(page, origin, c) {
  const booted = c.js ? page.waitForResponse((r) => /\/boot\.[^/]+\.js$/.test(new URL(r.url()).pathname)) : null;
  booted?.catch(() => {});
  const res = await page.goto(origin + "/", { waitUntil: "load" });
  if (!res?.ok()) throw new Error(`HTTP ${res?.status()}`);
  if (booted) {
    await booted;
    await page.waitForLoadState("networkidle");
  }
  await page.evaluate(() => document.fonts.ready);
  return res;
}

/** waitForFunction that polls on a timer, not on animation frames: a page busy drawing WebGL gets frames slowly. */
const waitFn = (page, fn, arg, opts = {}) => page.waitForFunction(fn, arg, { polling: 100, ...opts });
/** In a browser launched WITH software WebGL, no WebGL is a failure, never a quiet change of path. */
async function mustGL(page, ctx, id) {
  const gl = await hasGL(page);
  if (!gl) bad(id, ctx, "WebGL is unavailable in a browser launched with software WebGL: the canvas waits and the draw-call proof cannot run, which is a failure here");
  return gl;
}
const hasGL = (page) => page.evaluate(() => !!document.createElement("canvas").getContext("webgl2"));
const draws = (page) => page.evaluate(() => window.__draw);
const rafs = (page) => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));

/** Wait until no WebGL draw call has been made for two 400ms samples in a row, at most LONG. */
async function drawsStopped(page, what) {
  const until = Date.now() + LONG;
  let quiet = 0;
  for (;;) {
    const a = await draws(page);
    await page.waitForTimeout(400);
    quiet = (await draws(page)) === a ? quiet + 1 : 0;
    if (quiet >= 2) return;
    if (Date.now() > until) throw new Error(`${what}: still drawing WebGL frames ${LONG / 1000}s after it should have stopped`);
  }
}

/* ------------------------------------------------------- first-screen */
async function checkFirstScreen(page, c) {
  const first = DOCKER_RUN.split("\n")[0];
  const measure = (name) =>
    page.evaluate((first) => {
      const vh = window.innerHeight;
      const bottom = (s) => document.querySelector(s)?.getBoundingClientRect().bottom ?? null;
      // The bottom of the first line of the command: a range over its first words.
      const code = document.querySelector("[data-hero] .hero-code pre code");
      let line = null;
      const node = code?.firstChild;
      if (node && node.nodeType === 3 && node.data.startsWith(first)) {
        const r = document.createRange();
        r.setStart(node, 0);
        r.setEnd(node, first.length);
        line = r.getBoundingClientRect().bottom;
      }
      return {
        vh,
        h1: bottom("[data-hero] h1"),
        cta: bottom('[data-hero] a.btn-primary[href="/download"]'),
        line,
        label: document.querySelector("[data-hero] .hero-code .code-label")?.textContent.replace(/\s+/g, " ").trim() ?? null,
        sw: document.documentElement.scrollWidth,
        iw: window.innerWidth,
        top: window.scrollY,
      };
    }, first);
  for (const key of CASE_ORDER) {
    // Choosing the example by hand also stops the hero stepping on its own.
    if (c.js) {
      await page.click(`#tab-${key}`);
      await waitFn(page, (k) => !document.querySelector(`#panel-${k}`)?.hidden, key);
    }
    await page.evaluate(() => window.scrollTo({ top: 0, behavior: "instant" }));
    const m = await measure(key);
    const at = `${c.name} / ${key}`;
    log.push(`first-screen [${at}] h1 ${m.h1?.toFixed(0)}, button ${m.cta?.toFixed(0)}, command line ${m.line?.toFixed(0)} (fold ${m.vh})`);
    if (m.h1 === null) bad("first-screen", at, "no [data-hero] h1");
    else if (m.h1 > m.vh) bad("first-screen", at, `the h1 ends at ${m.h1.toFixed(0)}, below the fold (${m.vh})`);
    if (m.cta === null) bad("first-screen", at, 'no [data-hero] a.btn-primary[href="/download"]');
    else if (m.cta > m.vh) bad("first-screen", at, `the Install button ends at ${m.cta.toFixed(0)}, below the fold (${m.vh})`);
    if (m.line === null) bad("first-screen", at, "the hero's Docker command does not start with its first line");
    else if (m.line > m.vh) bad("first-screen", at, `the first line of the Docker command ends at ${m.line.toFixed(0)}, below the fold (${m.vh})`);
    if (m.label !== "docker run") bad("first-screen", at, `the hero command's label is ${JSON.stringify(m.label)}, expected "docker run"`);
    if (m.sw > m.iw) bad("first-screen", at, `the page scrolls sideways: scrollWidth ${m.sw} > ${m.iw}`);
    if (!c.js) break; // no script, no tabs: the first example is all there is
  }
}

/* ------------------------------------------------------------ console */
/** Scroll top to bottom, pausing in every section, then wait for what the scroll set going. */
async function checkConsole(page, c) {
  const step = (y) => page.evaluate((y) => window.scrollTo({ top: y, behavior: "instant" }), y);
  const webgl = await mustGL(page, c.name, "console");
  for (const sel of SECTIONS) {
    const box = await page.evaluate((sel) => {
      const r = document.querySelector(sel)?.getBoundingClientRect();
      return r ? { top: r.top + window.scrollY, h: r.height } : null;
    }, sel);
    if (!box) {
      bad("console", c.name, `no ${sel} to scroll through`);
      continue;
    }
    const vh = c.height;
    // Down through the section a screenful at a time; each stop lets two frames paint.
    for (let y = box.top; y < box.top + box.h; y += vh * 0.8) {
      await step(y);
      await rafs(page);
    }
    if (sel === "#deck") {
      await step(box.top);
      // The 3D device is built after its model arrives; it must have, or its errors are not seen.
      if (webgl) await page.waitForSelector("[data-deck-box] canvas");
      await page.waitForLoadState("networkidle");
    }
  }
  await step(await page.evaluate(() => document.documentElement.scrollHeight));
  await page.waitForLoadState("networkidle");
  await rafs(page);
  const csp = await page.evaluate(() => window.__csp);
  for (const v of csp) bad("console", c.name, `securitypolicyviolation: ${v}`);
  log.push(`console [${c.name}] scrolled ${SECTIONS.length} sections, ${csp.length} policy violations`);
}

/* ----------------------------------------------------------- keyboard */
async function arrowsThroughTabs(page, c, tablist, what) {
  const tabs = await page.$$eval(`${tablist} [role="tab"][data-key]`, (els) => els.map((e) => e.dataset.key));
  if (tabs.length < 3) throw new Error(`${what}: ${tabs.length} tabs found, expected at least 3`);
  const tab = (k) => `${tablist} [role="tab"][data-key="${k}"]`;
  const read = async () => {
    return page.evaluate((tl) => {
      const sel = [...document.querySelectorAll(`${tl} [role="tab"]`)].filter((t) => t.getAttribute("aria-selected") === "true");
      const t = sel[0];
      const panel = t ? document.getElementById(t.getAttribute("aria-controls")) : null;
      return { n: sel.length, key: t?.dataset.key ?? null, focus: document.activeElement?.dataset?.key ?? "", shown: !!panel && !panel.hidden && getComputedStyle(panel).display !== "none" };
    }, tablist);
  };
  const expectKey = async (key, how) => {
    await waitFn(page, 
      ([tl, k]) => document.querySelector(`${tl} [role="tab"][data-key="${k}"]`)?.getAttribute("aria-selected") === "true",
      [tablist, key],
    );
    const s = await read();
    if (s.n !== 1) bad("keyboard", c.name, `${what}: ${how} left ${s.n} tabs selected`);
    if (s.focus !== key) bad("keyboard", c.name, `${what}: ${how} left focus on "${s.focus}", not "${key}"`);
    if (!s.shown) bad("keyboard", c.name, `${what}: ${how} did not show the panel of "${key}"`);
  };
  await page.focus(tab(tabs[0]));
  await page.keyboard.press("ArrowRight");
  await expectKey(tabs[1], "ArrowRight");
  await page.keyboard.press("End");
  await expectKey(tabs[tabs.length - 1], "End");
  await page.keyboard.press("ArrowRight");
  await expectKey(tabs[0], "ArrowRight from the last tab (wraps)");
  await page.keyboard.press("ArrowLeft");
  await expectKey(tabs[tabs.length - 1], "ArrowLeft from the first tab (wraps)");
  await page.keyboard.press("Home");
  await expectKey(tabs[0], "Home");
}

/** Tab order through the hero follows what is on screen: top to bottom, then left to right within a row. */
async function checkHeroTabOrder(page, c) {
  await page.evaluate(() => window.scrollTo({ top: 0, behavior: "instant" }));
  await page.focus('[data-case-tabs] [role="tab"][aria-selected="true"]');
  const seen = [];
  for (let i = 0; i < 25; i++) {
    const e = await page.evaluate(() => {
      const a = document.activeElement;
      if (!a || !a.closest("[data-hero]")) return null;
      const r = a.getBoundingClientRect();
      return { name: (a.id || a.textContent || a.tagName).trim().replace(/\s+/g, " ").slice(0, 24), top: r.top + window.scrollY, left: r.left + window.scrollX };
    });
    if (!e) break;
    if (seen.some((x) => x.name === e.name)) break;
    seen.push(e);
    await page.keyboard.press("Tab");
  }
  if (seen.length < 5) bad("keyboard", c.name, `only ${seen.length} focusable elements in the hero (${seen.map((x) => x.name).join(", ")}); expected the case tab, both buttons, Pause and Copy`);
  // Rows: elements whose tops are within 12px share a row (a wrapped button row stays one row).
  const sorted = [...seen].sort((a, b) => a.top - b.top);
  const rows = [];
  for (const e of sorted) {
    const row = rows.find((r) => Math.abs(r[0].top - e.top) < 12);
    if (row) row.push(e);
    else rows.push([e]);
  }
  const visual = rows.flatMap((r) => r.sort((a, b) => a.left - b.left)).map((e) => e.name);
  const focus = seen.map((e) => e.name);
  log.push(`keyboard [${c.name}] hero Tab order: ${focus.join(" > ")}`);
  if (focus.join("|") !== visual.join("|")) bad("keyboard", c.name, `the hero's Tab order (${focus.join(" > ")}) is not its on-screen order (${visual.join(" > ")})`);
}

/** Script-driven controls are offered once script runs: Pause is shown and enabled, every tour chapter button is enabled and rendered. */
async function checkScriptControls(page, c) {
  const live = await page.evaluate(() => {
    const on = (b) => !b.hidden && !b.disabled && b.getClientRects().length > 0;
    return { pause: [...document.querySelectorAll("[data-hero] [data-pause]")].map(on), chapters: [...document.querySelectorAll("[data-tour] [data-seek]")].map((b) => !b.disabled && b.getClientRects().length > 0) };
  });
  if (live.pause.length === 0 || live.pause.some((x) => !x)) bad("keyboard", c.name, "the Pause motion button is hidden or disabled after boot");
  if (live.chapters.length === 0 || live.chapters.some((x) => !x)) bad("keyboard", c.name, `${live.chapters.filter((x) => !x).length} of ${live.chapters.length} tour chapter buttons are disabled or not rendered after boot`);
}

/** The calculator's steppers at their limits: aria-disabled (still focusable, a screen reader can find them), not
 *  `disabled`, and pressing one at its limit changes nothing. */
async function checkCalcSteppers(page, c) {
  await page.locator("#calc").scrollIntoViewIfNeeded();
  for (const [k, dir] of [["dests", "-1"], ["dests", "1"], ["tracks", "-1"], ["tracks", "1"]]) {
    const r = await page.evaluate(
      ([k, dir]) => {
        const b = document.querySelector(`#calc [data-calc-step="${k}"][data-dir="${dir}"]`);
        const input = document.querySelector(`#calc [data-calc-in="${k}"]`);
        if (!b || !input) return null;
        const atLimit = (x) => x.getAttribute("aria-disabled") === "true" || x.disabled;
        for (let i = 0; i < 80 && !atLimit(b); i++) b.click();
        const at = input.value;
        b.click();
        return { limit: atLimit(b), ariaDisabled: b.getAttribute("aria-disabled"), disabled: b.disabled, at, after: input.value };
      },
      [k, dir],
    );
    const name = `${k} ${dir === "1" ? "+" : "-"}`;
    if (!r) bad("keyboard", c.name, `no calculator stepper ${name}`);
    else if (!r.limit) bad("keyboard", c.name, `the calculator stepper ${name} never reached its limit in 80 presses`);
    else {
      if (r.disabled) bad("keyboard", c.name, `the calculator stepper ${name} is \`disabled\` at its limit; it must be aria-disabled so it stays focusable`);
      if (r.ariaDisabled !== "true") bad("keyboard", c.name, `the calculator stepper ${name} has aria-disabled="${r.ariaDisabled}" at its limit, expected "true"`);
      if (r.at !== r.after) bad("keyboard", c.name, `pressing the calculator stepper ${name} at its limit changed the value (${r.at} -> ${r.after})`);
    }
  }
}

/** Tab through #how: a step whose link takes focus lights up, so every focused link is on screen (a box, inside the viewport). */
async function checkHowFocus(page, c) {
  await page.evaluate(() => {
    window.scrollTo({ top: document.querySelector("#how").getBoundingClientRect().top + window.scrollY - 200, behavior: "instant" });
    const h = document.querySelector("#h-how");
    h.tabIndex = -1;
    h.focus();
  });
  const links = await page.evaluate(() => [...document.querySelectorAll("#how a[href]")].filter((a) => getComputedStyle(a).display !== "none").length);
  if (links === 0) bad("keyboard", c.name, "no links in #how to Tab through");
  const reached = new Set();
  for (let i = 0; i < 60; i++) {
    await page.keyboard.press("Tab");
    // Focus lights the step and the page scrolls it in (smoothly, with motion on). Judge where the link comes to
    // rest, not where it is mid-scroll: wait until the scroll position and the focused box hold for ten frames.
    await page.evaluate(
      () =>
        new Promise((done) => {
          const t0 = performance.now();
          let last = "";
          let held = 0;
          const tick = () => {
            const r = document.activeElement?.getBoundingClientRect();
            const now = `${scrollY}|${r ? Math.round(r.top) : ""}`;
            held = now === last ? held + 1 : 0;
            last = now;
            if (held >= 10 || performance.now() - t0 > 4000) done();
            else requestAnimationFrame(tick);
          };
          requestAnimationFrame(tick);
        }),
    );
    const m = await page.evaluate(() => {
      const a = document.activeElement;
      if (!a || !a.closest("#how")) return { out: true };
      if (!(a instanceof HTMLAnchorElement)) return { out: false, link: false };
      const r = a.getBoundingClientRect();
      // A link clipped away by a collapsed step still has a box and still passes checkVisibility(); what tells
      // the two apart is whether the point at its centre is the link itself.
      const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return { out: false, link: true, href: a.getAttribute("href"), box: r.width > 0 && r.height > 0, inside: r.top >= 0 && r.left >= 0 && r.bottom <= innerHeight && r.right <= innerWidth, shown: a.checkVisibility({ opacityProperty: true, visibilityProperty: true }), hit: !!hit && (a === hit || a.contains(hit)) };
    });
    if (m.out) break;
    if (!m.link) continue;
    reached.add(m.href);
    if (!m.box || !m.shown) bad("keyboard", c.name, `the #how link ${m.href} has focus but no visible box`);
    else if (!m.inside) bad("keyboard", c.name, `the #how link ${m.href} has focus but is outside the viewport`);
    else if (!m.hit) bad("keyboard", c.name, `the #how link ${m.href} has focus but is clipped or covered (the point at its centre is not the link)`);
  }
  if (reached.size === 0) bad("keyboard", c.name, `Tab reached none of the ${links} links in #how`);
}

async function checkKeyboard(page, c) {
  await checkHeroTabOrder(page, c);
  await page.evaluate(() => window.scrollTo({ top: 0, behavior: "instant" }));
  await arrowsThroughTabs(page, c, "[data-case-tabs]", "the case tabs");
  const have = await page.$$eval("[data-install-tabs] [role=tab]", (t) => t.map((e) => e.dataset.key));
  for (const k of ["docker", "compose", "script"]) if (!have.includes(k)) bad("keyboard", c.name, `no install tab "${k}"`);
  await page.locator("[data-install-tabs]").scrollIntoViewIfNeeded();
  await arrowsThroughTabs(page, c, "[data-install-tabs]", "the install tabs");

  await checkScriptControls(page, c);
  await checkCalcSteppers(page, c);
  if (!c.phone) await checkHowFocus(page, c);

  // A matrix cell is a real button: Space flips aria-pressed, and flips it back.
  const cell = page.locator("#matrix button.mx-cell").first();
  await cell.scrollIntoViewIfNeeded();
  const pressed = () => cell.getAttribute("aria-pressed");
  const before = await pressed();
  if (before !== "true" && before !== "false") bad("keyboard", c.name, `a matrix cell has aria-pressed="${before}"`);
  await cell.focus();
  await page.keyboard.press("Space");
  await waitFn(page, ([b]) => document.querySelector("#matrix button.mx-cell")?.getAttribute("aria-pressed") !== b, [before]);
  await page.keyboard.press("Space");
  await waitFn(page, ([b]) => document.querySelector("#matrix button.mx-cell")?.getAttribute("aria-pressed") === b, [before]);

  // The Stream Deck legend: every control is in the tab order, and Enter does something.
  const controls = await page.$$eval("#deck .dk-acts button", (bs) => bs.map((b, i) => (b.dataset.key ? `key ${b.dataset.key}` : `${b.closest("[data-dial]")?.dataset.dial}:${b.dataset.op}`)));
  if (controls.length < 14) bad("keyboard", c.name, `${controls.length} legend controls found, expected the 7 keys and both dials' 4 buttons`);
  await page.locator("#deck .dk-acts").scrollIntoViewIfNeeded();
  const disabled = await page.$$eval("#deck .dk-acts button", (bs) => bs.filter((b) => b.disabled).length);
  if (disabled) bad("keyboard", c.name, `${disabled} legend controls are still disabled after boot`);
  // Tab from the control just before the legend, and collect what takes focus.
  await page.evaluate(() => {
    const prior = document.querySelector("#deck .dk-log");
    prior.tabIndex = -1;
    prior.focus();
  });
  const reached = new Set();
  for (let i = 0; i < 60; i++) {
    await page.keyboard.press("Tab");
    const id = await page.evaluate(() => {
      const a = document.activeElement;
      if (!a?.closest("#deck .dk-acts")) return a?.closest("#deck") ? "other" : "gone";
      return a.dataset.key ? `key ${a.dataset.key}` : `${a.closest("[data-dial]")?.dataset.dial}:${a.dataset.op}`;
    });
    if (id === "gone") break;
    if (id !== "other") reached.add(id);
  }
  for (const id of controls) if (!reached.has(id)) bad("keyboard", c.name, `legend control "${id}" is not reached by Tab`);
  // Enter on each control changes what the legend or its log says. The first
  // Go live is judged by the log line it writes; every control writes one or
  // changes its row, and the screen-reader line (data-sr) is that same event.
  const snap = () =>
    page.evaluate(() => {
      const t = (s) => document.querySelector(s)?.textContent ?? "";
      return [t("#deck [data-sr]"), t("#deck [data-log]"), [...document.querySelectorAll("#deck .dk-acts [data-desc], #deck .dk-acts [data-db]")].map((e) => e.textContent).join("|")].join("¦");
    });
  // Fail fast: once one control has timed out, the rest of the loop waits five seconds each, not sixty.
  let slow = false;
  const wait = () => (slow ? 5000 : LONG);
  for (const id of controls) {
    const sel = id.startsWith("key ") ? `#deck .dk-acts button[data-key="${id.slice(4)}"]` : `#deck .dk-acts [data-dial="${id.split(":")[0]}"] button[data-op="${id.split(":")[1]}"]`;
    const before = await snap();
    try {
      await page.focus(sel, { timeout: wait() });
    } catch {
      slow = true;
      bad("keyboard", c.name, `legend control "${id}" cannot be focused`);
      continue;
    }
    await page.keyboard.press("Enter");
    try {
      await waitFn(page, 
        ([before]) => {
          const t = (s) => document.querySelector(s)?.textContent ?? "";
          const now = [t("#deck [data-sr]"), t("#deck [data-log]"), [...document.querySelectorAll("#deck .dk-acts [data-desc], #deck .dk-acts [data-db]")].map((e) => e.textContent).join("|")].join("¦");
          return now !== before;
        },
        [before],
        { timeout: wait() },
      );
    } catch {
      slow = true;
      bad("keyboard", c.name, `Enter on the legend control "${id}" changed nothing`);
    }
    // A hold key (Stop all, Routing preset) finishes by itself a moment later, and until it has, the
    // next hold is refused by the page. Wait for the finish (the screen-reader line stops saying
    // "Holding") rather than for a time, so the next control is judged on its own.
    if (await page.locator(sel).locator(".hold").count()) {
      await waitFn(page, () => !/^Holding/.test(document.querySelector("#deck [data-sr]")?.textContent ?? ""), undefined, { timeout: wait() }).catch(() => {
        slow = true;
        bad("keyboard", c.name, `the hold on "${id}" never finished`);
      });
    }
  }
  log.push(`keyboard [${c.name}] ${controls.length} legend controls reached by Tab and operated by Enter`);
}

/* ---------------------------------------------------------- no-script */
async function checkNoScript(page, c) {
  const m = await page.evaluate(
    ([headings, docker]) => {
      const visible = (el) => !!el && getComputedStyle(el).visibility !== "hidden" && el.getClientRects().length > 0;
      const text = (s) => document.querySelector(s)?.textContent.replace(/\s+/g, " ").trim() ?? null;
      const sports = document.querySelector("#panel-sports");
      return {
        h1: text("[data-hero] h1"),
        h1Visible: visible(document.querySelector("[data-hero] h1")),
        sportsVisible: !!sports && !sports.hidden && getComputedStyle(sports).display !== "none",
        tiles: sports ? [...sports.querySelectorAll(".tile")].filter(visible).length : -1,
        docker: document.querySelector("#hero-docker")?.textContent ?? null,
        dockerVisible: visible(document.querySelector("#hero-docker")),
        headings: headings.map((s) => ({ s, t: text(s), v: visible(document.querySelector(s)) })),
        // What the page itself renders as section headings, found by structure, not from the list above.
        rendered: [...document.querySelectorAll("[data-hero] h1, main section[id] h2, [data-tour] h3")].filter((h) => visible(h) && h.textContent.trim()).length,
        install: [...document.querySelectorAll('a.btn-primary[href="/download"]')].map((a) => ({ in: a.closest("[data-hero]") ? "hero" : a.closest("#use") ? "use" : "elsewhere", v: visible(a), t: a.textContent.trim() })),
        installDocs: [...document.querySelectorAll('a[href="/docs/install"]')].some(visible),
        installPanels: [...document.querySelectorAll("#install [role=tabpanel] pre")].map((p) => visible(p)),
        motionOn: document.documentElement.classList.contains("motion-on"),
        pause: [...document.querySelectorAll("[data-hero] [data-pause]")].map((b) => ({ shown: visible(b) && !b.hidden, disabled: b.disabled })),
        chapters: [...document.querySelectorAll("[data-tour] [data-seek]")].map((b) => b.disabled),
        dockerInInstall: [...document.querySelectorAll("#install pre")].some((p) => p.textContent.includes(docker)),
      };
    },
    [HEADINGS, DOCKER_RUN.split("\n")[0]],
  );
  if (!m.h1) bad("no-script", c.name, "no [data-hero] h1");
  else if (!m.h1Visible) bad("no-script", c.name, "the h1 is not rendered");
  if (!m.sportsVisible) bad("no-script", c.name, "the sports panel is not shown");
  if (m.tiles !== EXPECT_DESTS) bad("no-script", c.name, `the sports panel shows ${m.tiles} destinations, expected ${EXPECT_DESTS}`);
  if (m.docker === null || m.docker !== DOCKER_RUN) bad("no-script", c.name, "the hero's Docker command is missing or is not the install.mjs text");
  else if (!m.dockerVisible) bad("no-script", c.name, "the hero's Docker command is not rendered");
  for (const h of m.headings) {
    if (!h.t) bad("no-script", c.name, `heading ${h.s} is missing`);
    else if (!h.v) bad("no-script", c.name, `heading ${h.s} ("${h.t}") is not rendered`);
  }
  if (m.rendered !== 11) bad("no-script", c.name, `the page renders ${m.rendered} non-empty section headings (h1, section h2s, the video tour's h3), expected eleven`);
  const where = (k) => m.install.filter((a) => a.in === k);
  for (const k of ["hero", "use"]) {
    if (where(k).length === 0) bad("no-script", c.name, `no Install polyemesis link (a.btn-primary[href="/download"]) in ${k}`);
    else if (!where(k).some((a) => a.v)) bad("no-script", c.name, `the ${k} Install link is not rendered`);
  }
  if (!m.installDocs) bad("no-script", c.name, "no rendered link to /docs/install (the install guide)");
  if (m.installPanels.length < 3 || m.installPanels.some((v) => !v)) bad("no-script", c.name, `the Install section shows ${m.installPanels.filter(Boolean).length} of ${m.installPanels.length} method commands; with no script each must be readable`);
  if (!m.dockerInInstall) bad("no-script", c.name, "the Install section does not show the Docker command");
  if (m.motionOn) bad("no-script", c.name, "html.motion-on is set with script off");
  // Controls that only script can drive are not offered with script off: Pause is hidden, the tour's chapter buttons disabled.
  if (m.pause.length === 0) bad("no-script", c.name, "no [data-hero] [data-pause] button in the markup (script un-hides it)");
  else if (m.pause.some((b) => b.shown)) bad("no-script", c.name, "the Pause motion button is shown with script off, where it can do nothing");
  if (m.chapters.length === 0) bad("no-script", c.name, "no [data-tour] [data-seek] chapter buttons in the markup");
  else if (m.chapters.some((d) => !d)) bad("no-script", c.name, "the video tour's chapter buttons are enabled with script off, where they can do nothing");
}

/* -------------------------------------------------------------- still */
/** Blocks that move when motion is allowed: [label, selector], every match measured. */
const BLOCKS = [
  ["hero", "[data-hero]"],
  ["how it works", "[data-how] .how-stage"],
  ["mix matrix", "[data-matrix]"],
  ["feature panel", "[data-panels] [data-panel]"],
  ["stream deck", "[data-deck]"],
];

/* The sticky nav and journey bar are drawn over whatever scrolls under them, and where they land on a
 * tall block depends on how the shot scrolled to it. They are not part of any block: hide them (the
 * layout stays) for the shot. */
// JPEG, not PNG: the same pixels give the same bytes, and encoding a 2,000px-tall block as PNG was a large share
// of the run on a 2-vCPU runner.
const SHOT_STYLE = "header, [data-journey] { visibility: hidden !important }";

/** A picture of a block, taken as ordinary viewport screenshots of one screenful each (scroll, clip to the block's
 *  columns, next screenful) and joined. An element screenshot of a block taller than the screen asks the browser to
 *  render beyond the viewport, which on a starved software-raster runner was not repeatable for the tall phone
 *  blocks (the how-it-works diagram, the matrix) even for a page that had not moved. */
async function shoot(page, el) {
  const start = await el.evaluate((e) => {
    const r = e.getBoundingClientRect();
    return { top: r.top + window.scrollY, h: r.height, vh: window.innerHeight };
  });
  const parts = [];
  for (let y = 0; y < start.h; y += start.vh) {
    await page.evaluate((top) => window.scrollTo({ top, behavior: "instant" }), start.top + y);
    await rafs(page);
    // Where the block is NOW (a pinned block moves with the scroll): the part of it on screen.
    const r = await el.evaluate((e) => {
      const b = e.getBoundingClientRect();
      const top = Math.max(0, b.top);
      return { x: Math.max(0, b.left), y: top, w: Math.min(b.width, window.innerWidth - Math.max(0, b.left)), h: Math.min(window.innerHeight, b.bottom) - top };
    });
    if (r.h < 1 || r.w < 1) continue;
    parts.push(await page.screenshot({ type: "jpeg", quality: 60, timeout: 120_000, style: SHOT_STYLE, clip: { x: r.x, y: r.y, width: r.w, height: r.h } }));
  }
  return Buffer.concat(parts);
}

const PIXEL_BUDGET_MS = 240_000;
/** First shots: every block scrolled to, its images and 3D canvas up, shot until two in a row agree. The second
 *  shots are taken after the idle window (13 s or more later) by compareShots. */
async function shootAll(page, at, list, webgl) {
  const started = Date.now();
  const shots = [];
  for (const [label, sel] of list) {
    const n = await page.locator(sel).count();
    if (n === 0) {
      bad("still", at, `no ${label} block (${sel}) to compare`);
      continue;
    }
    for (let i = 0; i < n; i++) {
      if (Date.now() - started > PIXEL_BUDGET_MS) {
        bad("still", at, `the pixel comparison used up its ${PIXEL_BUDGET_MS / 1000}s budget; ${label} ${i + 1} and everything after it was not shot`);
        return shots;
      }
      const el = page.locator(sel).nth(i);
      await el.scrollIntoViewIfNeeded();
      // A 3D canvas only exists once its module has loaded, which a scroll to it starts: wait for it. A
      // browser with no WebGL keeps the poster (and that is already a failure where WebGL is required).
      if (webgl && label === "hero") await page.waitForSelector("[data-globe] canvas");
      if (webgl && label === "stream deck") await page.waitForSelector("[data-deck-box] canvas");
      await waitFn(page, (args) => [...[...document.querySelectorAll(args.sel)][args.i].querySelectorAll("img")].every((m) => m.complete || !m.checkVisibility()), { sel, i });
      // A tall block is rasterised in tiles that arrive over a few frames, so the first shots of a block that is
      // NOT animating can differ: shoot until two in a row agree.
      let prev = await shoot(page, el);
      let settled = false;
      for (let t = 0; t < 24 && !settled; t++) {
        await page.waitForTimeout(250);
        const cur = await shoot(page, el);
        settled = cur.equals(prev);
        prev = cur;
      }
      if (!settled) {
        bad("still", at, `${label} ${i + 1} (${sel}) never stopped changing`);
        continue;
      }
      shots.push({ label, sel, i });
    }
  }
  return shots;
}

/** Looks taken after the idle window. A block must reach TWO CONSECUTIVE IDENTICAL looks (each compared with the one
 *  before it, 600 ms apart, up to ten looks) and then be unchanged in a look two seconds later: a block that is still
 *  settles at once, software raster that was late settles within a few looks, and a block that keeps changing never
 *  does and fails by saying so. (Matching ANY one look would let a loop of a few seconds agree by chance.)
 *
 *  It does not compare with the shot taken BEFORE the window. Across the window's viewport round trip that differed
 *  in tall phone blocks on a starved 2-vCPU runner with nothing having moved (round 3). Taking both pictures inside
 *  the window's own viewport state, with no resize, scroll or pointer move between them, was tried in round 4 and
 *  also failed there on a still page: the how-it-works SVG diagrams' dashed strokes and box edges re-rasterised
 *  partway through (max channel delta 5, then 16; no text, no image), in 2 of 2 runs. What spans the window is the
 *  DOM watch, the animation watch, the WebGL and 2D draw counters, the video, SMIL and animated-image checks. The
 *  before-shots stay, to prove each block was already still before the window began. */
async function compareShots(page, at, shots) {
  let same = 0;
  for (const { label, sel, i } of shots) {
    const el = page.locator(sel).nth(i);
    await el.scrollIntoViewIfNeeded();
    if (label === "hero" || label === "stream deck") await drawsStopped(page, label);
    let prev = null;
    let settled = null;
    for (let t = 0; t < 10 && !settled; t++) {
      if (t) await page.waitForTimeout(600);
      const cur = await shoot(page, el);
      if (prev && cur.equals(prev)) settled = cur;
      prev = cur;
    }
    if (!settled) {
      bad("still", at, `${label} ${i + 1} (${sel}) never settled after the idle window: ten looks 600 ms apart and no two in a row alike`);
      continue;
    }
    await page.waitForTimeout(2000);
    const later = await shoot(page, el);
    if (later.equals(settled)) same++;
    else {
      bad("still", at, `${label} ${i + 1} (${sel}) changed in the two seconds after it had settled, after the idle window`);
    }
  }
  return same;
}

/* ------------------------------------------------------------ idle window */
// A window longer than the longest period of any loop on the page. The periods, read from the code:
//   hero auto-advance 12 s (hero.ts ADVANCE_MS), the hero's meters every 100 ms and its clock every 1 s,
//   the failover run 0.7 s lead-in + 2.6 + 1.8 + 3.4 s of stages (failover.ts), the feature panels
//   420 ms to 1.9 s (panels.ts LOOPS), the Stream Deck show 26 s long but ticking its clock every
//   100 ms (deck/index.ts), the globe turning every frame. 13 s clears the 12 s hero step.
const IDLE_MS = 13_000;

/** The batches of blocks that can be on screen together, so one window covers many blocks at once. A phone
 *  stacks everything and a tall viewport shows it all; a wide screen pins the how-it-works stage to the
 *  viewport (300vh track), so it takes two batches. */
function batchesFor(c) {
  const by = (l) => BLOCKS.find(([x]) => x === l);
  if (c.phone) return [{ height: "all", anchor: null, blocks: BLOCKS }];
  return [
    { height: 1800, anchor: null, blocks: [by("hero"), by("how it works")] },
    { height: 6500, anchor: "#matrix", blocks: [by("mix matrix"), by("feature panel"), by("stream deck")] },
  ];
}

/* ------------------------------------------------- pixel-only motion */
const imageVerdicts = new Map();
/** Whether an image resource can animate, read from its header bytes (a header check, not a decoder):
 *  - GIF: always counted as animated (frames are not counted);
 *  - PNG: animated if an acTL chunk comes before the first IDAT (APNG);
 *  - WebP: animated if the VP8X header sets the animation flag or there is an ANIM/ANMF chunk;
 *  - AVIF/HEIF: animated if the ftyp brands include "avis" (an image sequence);
 *  - SVG: animated if its text has a SMIL element or a CSS animation;
 *  - JPEG: cannot animate.
 *  Returns null for a still image, else why it is, or may be, animated. A resource that cannot be read (not served
 *  from dist/, not a data: URL) or is in no format above is reported, never passed. */
function animatedImage(url, origin) {
  if (!imageVerdicts.has(url)) {
    let v;
    try {
      v = sniffAnimation(imageBytes(url, origin));
    } catch (e) {
      v = `could not be read to rule animation out (${e.message})`;
    }
    imageVerdicts.set(url, v);
  }
  return imageVerdicts.get(url);
}
function imageBytes(url, origin) {
  if (url.startsWith("data:")) {
    const comma = url.indexOf(",");
    const body = url.slice(comma + 1);
    return url.slice(0, comma).endsWith(";base64") ? Buffer.from(body, "base64") : Buffer.from(decodeURIComponent(body));
  }
  const u = new URL(url);
  if (u.origin !== origin) throw new Error(`not served from the site but from ${u.origin}`);
  const f = distFile(decodeURIComponent(u.pathname));
  if (!f) throw new Error("no such file in dist/");
  return readFileSync(f);
}
function sniffAnimation(b) {
  const ascii = (o, n) => b.toString("latin1", o, o + n);
  if (ascii(0, 3) === "GIF") return "a GIF (every GIF counts as animated: frames are not counted)";
  if (b[0] === 0xff && b[1] === 0xd8) return null;
  if (b.length >= 8 && b.readUInt32BE(0) === 0x89504e47) {
    for (let o = 8; o + 8 <= b.length; o += 12 + b.readUInt32BE(o)) {
      const type = ascii(o + 4, 4);
      if (type === "acTL") return "an animated PNG (acTL chunk)";
      if (type === "IDAT" || type === "IEND") return null;
    }
    return "a PNG with no image data";
  }
  if (ascii(0, 4) === "RIFF" && ascii(8, 4) === "WEBP") {
    if (ascii(12, 4) === "VP8X" && b[20] & 0x02) return "an animated WebP (VP8X animation flag)";
    for (let o = 12; o + 8 <= b.length; ) {
      if (["ANIM", "ANMF"].includes(ascii(o, 4))) return "an animated WebP (ANIM chunk)";
      const len = b.readUInt32LE(o + 4);
      o += 8 + len + (len & 1);
    }
    return null;
  }
  if (ascii(4, 4) === "ftyp") {
    const end = Math.min(b.length, b.readUInt32BE(0));
    const brands = [ascii(8, 4)];
    for (let o = 16; o + 4 <= end; o += 4) brands.push(ascii(o, 4));
    if (brands.includes("avis")) return "an AVIF image sequence (ftyp brand avis)";
    if (brands.some((x) => ["avif", "mif1", "heic", "heix"].includes(x))) return null;
    return `an ISO media file with brands ${brands.join(",")}`;
  }
  const text = b.toString("utf8", 0, Math.min(b.length, 1 << 20));
  if (/<svg[\s>]/.test(text)) return /<(animate|animateTransform|animateMotion|animateColor|set)[\s/>]|@keyframes|animation(-name)?\s*:/.test(text) ? "an SVG with SMIL or CSS animation in it" : null;
  return "in an image format this check does not recognise";
}

/** For IDLE_MS, with every block in the batch on screen at once, in one viewport state:
 *  - no DOM mutation (attributes, children, text, any depth) inside a block, no running CSS animation or transition
 *    on anything inside it, no WebGL draw call and no 2D canvas draw call anywhere on the page;
 *  - every <video> on the page paused at both ends with the same currentTime, and no play event between;
 *  - no SMIL animation element on the page whose <svg> is not paused, at either end;
 *  - no image in a block (img, SVG image, CSS background/mask/border/list/content images) in an animated format.
 *  All are independent of frame rate. (Pictures of the blocks are NOT compared across the window: see compareShots.) */
async function idleWindow(page, c, at, batch, note) {
  const t0w = Date.now();
  let height = batch.height;
  if (height === "all") height = await page.evaluate(() => Math.ceil(document.querySelector("#deck").getBoundingClientRect().bottom + window.scrollY) + 400);
  await page.setViewportSize({ width: c.width, height });
  await rafs(page);
  await page.evaluate((anchor) => {
    const el = anchor ? document.querySelector(anchor) : null;
    window.scrollTo({ top: el ? el.getBoundingClientRect().top + window.scrollY - 70 : 0, behavior: "instant" });
  }, batch.anchor);
  await rafs(page);
  await drawsStopped(page, `${note} window`);
  await page.waitForTimeout(500); // whatever the resize itself set going has finished
  // The tall viewport brings lazy images into range all at once: every visible image in the batch loaded and
  // decoded, and the fonts settled, before the watch begins, so nothing finishes arriving during the window.
  const sels = batch.blocks.map(([, s]) => s);
  await waitFn(page, (sels) => sels.every((s) => [...document.querySelectorAll(s)].every((b) => [...b.querySelectorAll("img")].every((m) => m.complete || !m.checkVisibility()))), sels);
  await page.evaluate(async (sels) => {
    await document.fonts.ready;
    const imgs = sels.flatMap((s) => [...document.querySelectorAll(s)].flatMap((b) => [...b.querySelectorAll("img")])).filter((m) => m.checkVisibility());
    await Promise.all(imgs.map((m) => m.decode().catch(() => {})));
  }, sels);
  await rafs(page);
  await page.evaluate((blocks) => {
    // The share of a block that is inside the viewport: the page's loops run only for blocks on screen.
    window.__onScreen = (el) => {
      const r = el.getBoundingClientRect();
      const w = Math.max(0, Math.min(r.right, innerWidth) - Math.max(r.left, 0));
      const h = Math.max(0, Math.min(r.bottom, innerHeight) - Math.max(r.top, 0));
      return r.width * r.height > 0 ? (w * h) / (r.width * r.height) : 0;
    };
    const st = (window.__still = { recs: [], timer: 0, pageAnims: new Set() });
    // A short selector for an element: up to four ancestors, stopping at the first id.
    const selectorOf = (el) => {
      const parts = [];
      for (let n = el; n && n.nodeType === 1 && parts.length < 4; n = n.parentElement) {
        const cls = typeof n.className === "string" && n.className.trim() ? "." + n.className.trim().split(/\s+/)[0] : "";
        parts.unshift(`${n.localName}${n.id ? "#" + n.id : ""}${cls}`);
        if (n.id) break;
      }
      return parts.join(" > ");
    };
    for (const [label, sel] of blocks) {
      [...document.querySelectorAll(sel)].forEach((el, i) => {
        const rec = { name: `${label} ${i + 1}`, el, n: 0, first: [], anims: new Set(), v0: window.__onScreen(el), v1: 1 };
        rec.mo = new MutationObserver((list) => {
          for (const m of list) {
            rec.n++;
            if (rec.first.length < 3) rec.first.push(`${m.type} on <${m.target.nodeName.toLowerCase()}${m.target.className && typeof m.target.className === "string" ? "." + m.target.className.split(" ")[0] : ""}>${m.attributeName ? " " + m.attributeName : ""}`);
          }
        });
        rec.mo.observe(el, { attributes: true, childList: true, characterData: true, subtree: true });
        st.recs.push(rec);
      });
    }
    st.timer = setInterval(() => {
      for (const a of document.getAnimations()) {
        if (a.playState !== "running") continue;
        const t = a.effect?.target;
        if (!t) continue;
        for (const r of st.recs) if (r.el.contains(t)) r.anims.add(a.animationName || a.transitionProperty || a.id || "animation");
        // Page-wide: a running animation anywhere (nav, journey bar, #why, #install, #calc, footer), not only in the watched
        // blocks. A scroll-linked animation is driven by the scroll position, not by time, so it is not a loop.
        if (typeof ScrollTimeline !== "undefined" && a.timeline instanceof ScrollTimeline) continue;
        if (st.recs.some((r) => r.el.contains(t))) continue; // already reported, by block, above
        st.pageAnims.add(`${selectorOf(t)}${a.effect.pseudoElement ?? ""} (${a.animationName || a.transitionProperty || a.id || "animation"})`);
      }
    }, 250);
    // Pixel-only motion, read at its sources: videos, SMIL, 2D canvases (images are read at the end).
    const blockOf = (n) => st.recs.find((r) => r.el.contains(n))?.name ?? "outside the watched blocks";
    st.vids = () =>
      [...document.querySelectorAll("video")].map((v) => ({
        v,
        name: `<video${v.className ? "." + String(v.className).split(" ")[0] : ""} ${v.currentSrc || v.querySelector("source")?.getAttribute("src") || v.getAttribute("src") || "(no source)"}> (${blockOf(v)})`,
        paused: v.paused,
        t: v.currentTime,
      }));
    st.smil = () =>
      [...document.getElementsByTagNameNS("http://www.w3.org/2000/svg", "*")]
        .filter((e) => e instanceof SVGAnimationElement && !(e.ownerSVGElement?.animationsPaused() ?? false))
        .map((e) => `<${e.localName}> (${blockOf(e)})`);
    st.canvasName = (cv) => (cv instanceof HTMLCanvasElement ? `canvas ${cv.width}x${cv.height} ${cv.isConnected ? `in the page (${blockOf(cv)})` : "not in the document (a texture source)"}` : `OffscreenCanvas ${cv.width}x${cv.height}`);
    st.v0 = st.vids();
    st.plays = new Map();
    st.onPlay = (e) => st.plays.set(e.target, (st.plays.get(e.target) ?? 0) + 1);
    for (const { v } of st.v0) for (const ev of ["play", "playing", "seeked"]) v.addEventListener(ev, st.onPlay);
    st.smil0 = st.smil();
    st.c2d0 = new Map(window.__c2d);
    st.d2d0 = window.__draw2d;
  }, batch.blocks);
  const d0 = await draws(page);
  const tw = Date.now();
  await page.waitForTimeout(IDLE_MS);
  const d1 = await draws(page);
  const res = await page.evaluate(() => {
    const st = window.__still;
    clearInterval(st.timer);
    const blocks = st.recs.map((r) => {
      const left = r.mo.takeRecords().length;
      r.mo.disconnect();
      return { name: r.name, n: r.n + left, first: r.first, anims: [...r.anims], v0: r.v0, v1: window.__onScreen(r.el) };
    });
    const v1 = st.vids();
    const videos = st.v0.map((a) => {
      const b = v1.find((x) => x.v === a.v);
      for (const ev of ["play", "playing", "seeked"]) a.v.removeEventListener(ev, st.onPlay);
      return { name: a.name, p0: a.paused, p1: b?.paused ?? null, t0: a.t, t1: b?.t ?? null, plays: st.plays.get(a.v) ?? 0, ready: a.v.readyState, mp4: a.v.canPlayType('video/mp4; codecs="avc1.42E01E"') || "no" };
    });
    const canvases = [];
    for (const [cv, n] of window.__c2d) {
      const d = n - (st.c2d0.get(cv) ?? 0);
      if (d) canvases.push(`${d} on ${st.canvasName(cv)}`);
    }
    // Every image a block shows: <img> (its chosen source), SVG <image>, and CSS images on elements and pseudo-elements.
    const images = new Set();
    const PROPS = ["background-image", "mask-image", "-webkit-mask-image", "border-image-source", "list-style-image"];
    for (const r of st.recs) {
      for (const el of [r.el, ...r.el.querySelectorAll("*")]) {
        if (el instanceof HTMLImageElement && (el.currentSrc || el.src)) images.add(`${r.name}\n${el.currentSrc || el.src}`);
        if (el instanceof SVGImageElement && el.href.baseVal) images.add(`${r.name}\n${new URL(el.href.baseVal, document.baseURI).href}`);
        for (const pseudo of [null, "::before", "::after"]) {
          const cs = getComputedStyle(el, pseudo);
          for (const p of pseudo ? [...PROPS, "content"] : PROPS) {
            for (const m of cs.getPropertyValue(p).matchAll(/url\("?([^")]+)"?\)/g)) images.add(`${r.name}\n${new URL(m[1], document.baseURI).href}`);
          }
        }
      }
    }
    return { pageAnims: [...st.pageAnims], blocks, videos, smil0: st.smil0, smil1: st.smil(), d2d: window.__draw2d - st.d2d0, canvases, images: [...images].map((x) => x.split("\n")) };
  });
  const sweep = ((Date.now() - tw) / 1000 - IDLE_MS / 1000).toFixed(1);
  await page.setViewportSize({ width: c.width, height: c.height });
  await rafs(page);
  const blocks = res.blocks;
  log.push(
    `still [${at}] window ${c.width}x${height} (${note}): ${blocks.length} blocks on screen, least visible ${Math.round(Math.min(...blocks.map((r) => Math.min(r.v0, r.v1))) * 100)}%; ` +
      `${res.d2d} 2D draws; ` +
      `videos ${res.videos.map((v) => `${v.name} paused ${v.p0}/${v.p1} t ${v.t0}/${v.t1} ready ${v.ready} mp4 ${v.mp4}`).join(", ")}; ` +
      `${res.smil0.length}/${res.smil1.length} SMIL; ${new Set(res.images.map(([, u]) => u)).size} images read; ` +
      `prep ${((tw - t0w) / 1000).toFixed(1)}s, sweep ${sweep}s`,
  );
  if (blocks.length === 0) bad("still", at, `${note}: the idle window found no block to watch`);
  const MIN_ON_SCREEN = 0.25; // enough of the block on screen to judge it (the loops start at 0.1, panels 0.25, failover 0.5)
  for (const r of blocks) {
    if (r.v0 < MIN_ON_SCREEN || r.v1 < MIN_ON_SCREEN) bad("still", at, `${note}: ${r.name} was not on screen for the idle window (${Math.round(r.v0 * 100)}% at the start, ${Math.round(r.v1 * 100)}% at the end), so its loops could not have run: the watch proves nothing for it`);
    if (r.n) bad("still", at, `${note}: ${r.name} had ${r.n} DOM mutation(s) in ${IDLE_MS / 1000}s idle (first: ${r.first.join("; ")})`);
    if (r.anims.length) bad("still", at, `${note}: ${r.name} had a running CSS animation or transition: ${r.anims.join(", ")}`);
  }
  if (res.pageAnims.length) bad("still", at, `${note}: running CSS animation(s) outside the watched blocks: ${res.pageAnims.slice(0, 6).join("; ")}`);
  if (d1 !== d0) bad("still", at, `${note}: ${d1 - d0} WebGL draw calls in ${IDLE_MS / 1000}s idle`);
  if (res.d2d) bad("still", at, `${note}: ${res.d2d} 2D canvas draw calls in ${IDLE_MS / 1000}s idle (${res.canvases.slice(0, 4).join("; ")})`);
  if (res.videos.length === 0) bad("still", at, `${note}: no <video> on the page (the tour): nothing to prove paused`);
  for (const v of res.videos) {
    if (!v.p0 || !v.p1) bad("still", at, `${note}: ${v.name} was playing at the ${!v.p0 ? "start" : "end"} of the idle window (paused ${v.p0} -> ${v.p1})`);
    if (v.t0 !== v.t1) bad("still", at, `${note}: ${v.name} moved from ${v.t0}s to ${v.t1}s during the idle window`);
    if (v.plays) bad("still", at, `${note}: ${v.name} fired ${v.plays} play/playing/seeked event(s) during the idle window`);
  }
  for (const [when, list] of [["start", res.smil0], ["end", res.smil1]]) if (list.length) bad("still", at, `${note}: ${list.length} SVG SMIL animation element(s) not paused at the ${when} of the idle window: ${list.slice(0, 4).join(", ")}`);
  const origin = new URL(page.url()).origin;
  for (const [block, url] of res.images) {
    const why = animatedImage(url, origin);
    if (why) bad("still", at, `${note}: ${block} shows ${url.length > 120 ? url.slice(0, 120) + "..." : url}, ${why}`);
  }
  return blocks.length;
}

async function idleWindows(page, c, at, note) {
  let watched = 0;
  for (const batch of batchesFor(c)) watched += await idleWindow(page, c, at, batch, note);
  return watched;
}

/** Stillness: pixels shot until settled, then a long idle window of DOM, animation, draw-call, video, SMIL and image
 *  watching, then pixels settled again; on a phone again with every fold opened (the miniatures the folds hide). */
async function checkStill(page, c, why) {
  const at = `${c.name} ${why}`;
  const webgl = await mustGL(page, at, "still");
  const motionOn = await page.evaluate(() => document.documentElement.classList.contains("motion-on"));
  if (motionOn) bad("still", at, "html.motion-on is set where motion must be off");
  // The pointer rests on the Pause button after Pause was clicked; as the page scrolls under that spot a
  // different matrix cell is :hover (a border colour) before and after the idle window. Park it off the page content.
  await page.mouse.move(0, 0);
  const before = await shootAll(page, at, BLOCKS, webgl);
  let watched = await idleWindows(page, c, at, "folds as shipped");
  await page.mouse.move(0, 0);
  let same = await compareShots(page, at, before);
  let extra = "";
  if (c.phone) {
    const folds = await page.evaluate(() => {
      const f = [...document.querySelectorAll("details.fold")];
      for (const d of f) d.open = true;
      return f.length;
    });
    if (folds === 0) bad("still", at, "no phone folds to open");
    await rafs(page);
    const panels = [BLOCKS.find(([l]) => l === "feature panel")];
    const opened = await shootAll(page, `${at} folds open`, panels, webgl);
    watched += await idleWindows(page, c, `${at} folds open`, "folds open");
    same += await compareShots(page, `${at} folds open`, opened);
    extra = `, then ${folds} folds opened and watched again`;
  }
  log.push(`still [${at}] ${same} blocks settled and unchanged after the idle window; ${watched} block-windows of ${IDLE_MS / 1000}s with 0 DOM mutations, 0 running animations, 0 WebGL or 2D draw calls, videos paused, no SMIL or animated image${extra} (WebGL ${webgl ? "on" : "OFF"})`);
}

/** With motion on, something in the hero really does move: the proof that "identical" is not just a still page. */
async function motionIsLive(page, c) {
  await page.evaluate(() => window.scrollTo({ top: 0, behavior: "instant" }));
  const clock = () => page.evaluate(() => document.querySelector("[data-hero] [data-clock]")?.textContent ?? null);
  const a = await clock();
  if (a === null) throw new Error("no [data-hero] [data-clock]");
  try {
    await waitFn(page, (a) => document.querySelector("[data-hero] [data-clock]")?.textContent !== a, a);
  } catch {
    const st = await page.evaluate(() => ({ on: document.documentElement.classList.contains("motion-on"), y: scrollY, vis: document.visibilityState, clk: document.querySelector("[data-hero] [data-clock]")?.textContent }));
    throw new Error(`the hero's ON AIR clock never ticked with motion on (${JSON.stringify(st)})`);
  }
  if (await mustGL(page, c.name, "still")) {
    await page.waitForSelector("[data-globe] canvas");
    // On a phone the globe is below the first screen, and it only draws while enough of it is on screen
    // (whileVisible honours its threshold): centre it rather than nudging it to an edge.
    await page.evaluate(() => document.querySelector("[data-globe]").scrollIntoView({ behavior: "instant", block: "center" }));
    const d = await draws(page);
    await waitFn(page, (d) => window.__draw > d, d);
  }
}

async function checkPause(page, c) {
  await motionIsLive(page, c);
  const btn = page.locator("[data-hero] [data-pause]");
  await btn.click();
  await waitFn(page, () => !document.documentElement.classList.contains("motion-on"));
  if ((await btn.getAttribute("aria-pressed")) !== "true") bad("still", c.name, 'Pause did not set aria-pressed="true"');
  await checkStill(page, c, "after Pause");
}

/* ------------------------------------------------------------ lazy-3d */
/** Poll `cond` (node side) every 100ms for at most LONG. */
async function until(cond, what) {
  const end = Date.now() + LONG;
  while (!cond()) {
    if (Date.now() > end) throw new Error(what);
    await new Promise((r) => setTimeout(r, 100));
  }
}

/** A request log in the order the page made them. */
function recordRequests(page) {
  const seen = [];
  page.on("request", (r) => seen.push(new URL(r.url()).pathname));
  return seen;
}
const isThree = (p) => /\/three[.-]/.test(p) || /\/three\.module/.test(p);
const isScene = (p) => /\/scene\.[^/]+\.js$/.test(p);

async function checkLazy3d(browser, origin, c) {
  const { ctx, page } = await open(browser, c);
  try {
    const seen = recordRequests(page);
    const booted = page.waitForResponse((r) => /\/boot\.[^/]+\.js$/.test(new URL(r.url()).pathname));
    booted.catch(() => {});
    const res = await page.goto(origin + "/", { waitUntil: "load" });
    const html = await res.text();
    const tags = html.match(/<(?:script|link)\b[^>]*>/g) ?? [];
    const early = tags.filter((t) => /three|\.glb|scene\./i.test(t));
    if (early.length) bad("lazy-3d", c.name, `the initial HTML loads 3D code up front: ${early[0].slice(0, 120)}`);
    if (!tags.some((t) => /<script\b[^>]*type="module"/.test(t))) bad("lazy-3d", c.name, "the initial HTML has no module script to check");
    await booted;
    const bootAt = seen.findIndex((p) => /\/boot\.[^/]+\.js$/.test(p));
    if (bootAt < 0) throw new Error("boot chunk never requested");
    const threeBeforeBoot = seen.slice(0, bootAt).filter(isThree);
    if (threeBeforeBoot.length) bad("lazy-3d", c.name, `three.js was requested before boot: ${threeBeforeBoot[0]}`);
    // The hero is on the first screen, so its globe loads three.js soon after boot: wait for it.
    await until(() => seen.some(isThree), "three.js was never requested, though the hero is on the first screen");
    const webgl = await mustGL(page, c.name, "lazy-3d");
    if (webgl) await page.waitForSelector("[data-globe] canvas");
    await page.waitForLoadState("networkidle");
    // Held at the top: an absence, so a window, not a wait for something.
    await page.evaluate(() => window.scrollTo({ top: 0, behavior: "instant" }));
    await page.waitForTimeout(2000);
    const glbAtTop = seen.filter((p) => p === MODEL).length;
    const sceneAtTop = seen.filter(isScene).length;
    if (glbAtTop !== 0) bad("lazy-3d", c.name, `${MODEL} was requested ${glbAtTop} time(s) with the page held at the top`);
    if (sceneAtTop !== 0) bad("lazy-3d", c.name, "the Stream Deck scene module was requested with the page held at the top");
    // Then #deck approaches: the model is fetched, once.
    await page.evaluate(() => document.querySelector("#deck").scrollIntoView({ behavior: "instant", block: "start" }));
    await until(() => seen.includes(MODEL), `${MODEL} was never requested after #deck came near`);
    if (webgl) await page.waitForSelector("[data-deck-box] canvas");
    await page.waitForLoadState("networkidle");
    await page.waitForTimeout(1500);
    const glb = seen.filter((p) => p === MODEL).length;
    if (glb !== 1) bad("lazy-3d", c.name, `${MODEL} was requested ${glb} times after #deck came near, expected exactly 1`);
    log.push(`lazy-3d [${c.name}] three.js at request #${seen.findIndex(isThree)} (boot #${bootAt}); model 0 at the top, ${glb} after #deck`);
  } finally {
    await ctx.close();
  }
}

async function checkNoWebGL(browser, origin, c) {
  const { ctx, page } = await open(browser, c);
  try {
    const seen = recordRequests(page);
    await load(page, origin, c);
    if (await hasGL(page)) bad("lazy-3d", `${c.name} no-webgl`, "WebGL is available in the browser that was meant to have none");
    await page.evaluate(() => document.querySelector("#deck").scrollIntoView({ behavior: "instant", block: "start" }));
    // The deck's module is imported when #deck is near, then finds no WebGL and leaves the still.
    // Not optional: if the scene module was never requested, nothing exercised the no-WebGL path and the checks
    // below would pass on the page's initial state, so that is a failure of its own.
    try {
      await until(() => seen.some(isScene), "the Stream Deck scene module was never requested after #deck came near");
    } catch (e) {
      bad("lazy-3d", `${c.name} no-webgl`, `${e.message}: the no-WebGL fallback was not exercised`);
    }
    await page.waitForLoadState("networkidle");
    await page.waitForTimeout(1000);
    const m = await page.evaluate(() => {
      const img = (s) => {
        const i = document.querySelector(s);
        const r = i?.getBoundingClientRect();
        // Drawn means: loaded, a box, and not display:none, visibility:hidden or opacity:0 on it or any ancestor.
        const seen = !!i && i.checkVisibility({ opacityProperty: true, visibilityProperty: true });
        return i ? { ok: i.complete && i.naturalWidth > 0, shown: !!r && r.width > 0 && r.height > 0 && seen } : null;
      };
      return {
        globePoster: img("[data-globe] img"),
        globeCanvas: !!document.querySelector("[data-globe] canvas"),
        deckPoster: img("[data-deck-box] img"),
        deckCanvas: !!document.querySelector("[data-deck-box] canvas"),
      };
    });
    const at = `${c.name} no-webgl`;
    if (!m.globePoster) bad("lazy-3d", at, "the hero globe's poster is gone");
    else if (!m.globePoster.ok || !m.globePoster.shown) bad("lazy-3d", at, "the hero globe's poster is not displayed");
    if (m.globeCanvas) bad("lazy-3d", at, "a canvas replaced the hero globe's poster with WebGL off");
    if (!m.deckPoster) bad("lazy-3d", at, "the Stream Deck still is gone");
    else if (!m.deckPoster.ok || !m.deckPoster.shown) bad("lazy-3d", at, "the Stream Deck still is not displayed");
    if (m.deckCanvas) bad("lazy-3d", at, "a canvas replaced the Stream Deck still with WebGL off");
    if (seen.includes(MODEL)) bad("lazy-3d", at, "the model was fetched with no WebGL to draw it");
    log.push(`lazy-3d [${at}] both posters stay, no canvas, console clean`);
  } finally {
    await ctx.close();
  }
}

/** The Stream Deck model fails to load (its request is aborted): the still stays, the legend still drives the log, and
 *  nothing is reported but the aborted request itself. Its own context, not `open()`, because `open()` fails on any
 *  console error and this one has exactly one expected. */
async function checkModelFails(browser, origin, c) {
  const at = `${c.name} model-fail`;
  const ctx = await browser.newContext({ viewport: { width: c.width, height: c.height }, reducedMotion: "no-preference" });
  try {
    const page = await ctx.newPage();
    page.setDefaultTimeout(LONG);
    // The one error this case expects is the browser's own failed-load line for the model. Anything else is a
    // failure, including a failed load of any other file and an uncaught error that does not name the model.
    const model = /stream-deck-plus/i;
    page.on("console", (msg) => {
      if (msg.type() === "error" && !model.test(msg.location().url) && !model.test(msg.text())) bad("model-fail", at, `console error: ${msg.text()}`);
    });
    page.on("pageerror", (err) => {
      if (!model.test(err.message)) bad("model-fail", at, `uncaught: ${err.message}`);
    });
    await page.route(`**${MODEL}`, (r) => r.abort());
    const seen = recordRequests(page);
    await load(page, origin, c);
    await page.evaluate(() => document.querySelector("#deck").scrollIntoView({ behavior: "instant", block: "start" }));
    await until(() => seen.includes(MODEL), `${MODEL} was never requested after #deck came near, so the failure was not exercised`);
    await page.waitForLoadState("networkidle");
    await page.waitForTimeout(1500);
    const m = await page.evaluate(() => {
      const i = document.querySelector("[data-deck-box] img");
      const r = i?.getBoundingClientRect();
      return { img: i ? { ok: i.complete && i.naturalWidth > 0, shown: !!r && r.width > 0 && r.height > 0 && i.checkVisibility({ opacityProperty: true, visibilityProperty: true }) } : null, canvas: !!document.querySelector("[data-deck-box] canvas") };
    });
    if (!m.img) bad("model-fail", at, "the Stream Deck still is gone after the model failed to load");
    else if (!m.img.ok || !m.img.shown) bad("model-fail", at, "the Stream Deck still is not displayed after the model failed to load");
    if (m.canvas) bad("model-fail", at, "a canvas exists although the model never arrived");
    // The legend still works: Enter on the first key changes the log or the legend.
    const snap = () =>
      page.evaluate(() => {
        const t = (s) => document.querySelector(s)?.textContent ?? "";
        return [t("#deck [data-sr]"), t("#deck [data-log]"), [...document.querySelectorAll("#deck .dk-acts [data-desc], #deck .dk-acts [data-db]")].map((e) => e.textContent).join("|")].join("¦");
      });
    const before = await snap();
    await page.locator("#deck .dk-acts button[data-key]").first().focus();
    await page.keyboard.press("Enter");
    try {
      await waitFn(
        page,
        ([before]) => {
          const t = (s) => document.querySelector(s)?.textContent ?? "";
          return [t("#deck [data-sr]"), t("#deck [data-log]"), [...document.querySelectorAll("#deck .dk-acts [data-desc], #deck .dk-acts [data-db]")].map((e) => e.textContent).join("|")].join("¦") !== before;
        },
        [before],
        { timeout: 10_000 },
      );
    } catch {
      bad("model-fail", at, "Enter on a legend key changed nothing after the model failed to load");
    }
    log.push(`model-fail [${at}] model request aborted: still shown, no canvas, legend drives the log, no errors but the aborted request`);
  } finally {
    await ctx.close();
  }
}

/* ----------------------------------------------------------- contrast */
async function checkContrast(page, c) {
  const m = await page.evaluate(() => {
    const ctx = document.createElement("canvas").getContext("2d", { willReadFrequently: true });
    const rgb = (css) => {
      ctx.clearRect(0, 0, 1, 1);
      ctx.fillStyle = "#000";
      ctx.fillStyle = css;
      ctx.fillRect(0, 0, 1, 1);
      const d = ctx.getImageData(0, 0, 1, 1).data;
      return [d[0], d[1], d[2]];
    };
    const lum = ([r, g, b]) => {
      const f = (v) => ((v /= 255) <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
      return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
    };
    const ratio = (a, b) => {
      const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x);
      return (hi + 0.05) / (lo + 0.05);
    };
    const bg = rgb(getComputedStyle(document.body).backgroundColor);
    const read = (s) => {
      const el = document.querySelector(s);
      return el ? ratio(rgb(getComputedStyle(el).color), bg) : null;
    };
    return { h1: read("[data-hero] h1"), accent: read("[data-hero] h1 em"), lede: read("[data-hero] .hero-lede") };
  });
  log.push(`contrast [${c.name}] h1 ${m.h1?.toFixed(2)}:1, accent word ${m.accent?.toFixed(2)}:1, lede ${m.lede?.toFixed(2)}:1 (min ${MIN_CONTRAST})`);
  if (m.h1 === null) bad("contrast", c.name, "no [data-hero] h1");
  else if (m.h1 < MIN_CONTRAST) bad("contrast", c.name, `the h1 is ${m.h1.toFixed(2)}:1 on the page ground, under ${MIN_CONTRAST}:1`);
  if (m.lede === null) bad("contrast", c.name, "no [data-hero] .hero-lede");
  else if (m.lede < MIN_CONTRAST) bad("contrast", c.name, `the hero lede is ${m.lede.toFixed(2)}:1 on the page ground, under ${MIN_CONTRAST}:1`);
  // The coral accent word is display type at 56px or more: WCAG's large-text bar (3:1), not the 7:1 pin.
  if (m.accent === null) bad("contrast", c.name, "no [data-hero] h1 em accent word");
  else if (m.accent < 3) bad("contrast", c.name, `the h1's accent word is ${m.accent.toFixed(2)}:1, under the 3:1 large-text bar`);
}

/* ------------------------------------------------- matrix-phone, journey-bar */
const PHONES = [390, 360, 320].map((w) => ({ name: `${w} phone`, width: w, height: 800, js: true, reduced: false, phone: true }));

/** The mix matrix on a phone is cards, not a table in a scroll frame: nothing leaves the viewport, nothing scrolls
 *  sideways, every track toggle is a 44px+ control with text of 12px or more, and it works. */
async function checkMatrixPhone(page, c) {
  await page.locator("#matrix .mx-panel").scrollIntoViewIfNeeded();
  const m = await page.evaluate(() => {
    const vw = window.innerWidth;
    const out = [];
    for (const el of document.querySelectorAll("#matrix *")) {
      if (!el.checkVisibility()) continue;
      const r = el.getBoundingClientRect();
      if (r.width <= 1 || r.height <= 1) continue; // the clipped header row
      if (r.right > vw + 0.5 || r.left < -0.5) out.push(`<${el.tagName.toLowerCase()} class="${String(el.className).slice(0, 24)}"> spans ${r.left.toFixed(0)}..${r.right.toFixed(0)} in a ${vw}px viewport`);
    }
    const sc = document.querySelector("#matrix .mx-scroll");
    const cells = [...document.querySelectorAll("#matrix button.mx-cell")];
    const small = [];
    for (const el of document.querySelectorAll("#matrix .mx-panel *")) {
      if (!el.checkVisibility() || ![...el.childNodes].some((n) => n.nodeType === 3 && n.data.trim())) continue;
      const px = parseFloat(getComputedStyle(el).fontSize);
      if (px < 12) small.push(`${el.className || el.tagName} ${px}px`);
    }
    return {
      over: out.slice(0, 6),
      doc: document.documentElement.scrollWidth,
      vw,
      scrollW: sc ? [sc.scrollWidth, sc.clientWidth] : null,
      cells: cells.length,
      hidden: cells.filter((b) => !b.checkVisibility()).length,
      short: cells.filter((b) => b.getBoundingClientRect().height < 43.5).length,
      // The visible name is the track's (.mx-tn, shown in place of "sent"/"off"); the label read out must name
      // that track and the destination of the toggle's own row.
      tnHidden: cells.filter((b) => !b.querySelector(".mx-tn")?.checkVisibility()).map((b) => b.getAttribute("aria-label")),
      named: cells.filter((b) => {
        const label = b.getAttribute("aria-label") ?? "";
        const track = b.querySelector(".mx-tn")?.textContent.trim();
        const dest = b.closest('[role="row"]')?.querySelector('[role="rowheader"]')?.textContent.trim();
        return !!track && !!dest && label.startsWith(`${track} `) && label.endsWith(` to ${dest}`);
      }).length,
      small: small.slice(0, 4),
      cols: new Set(cells.slice(0, 6).map((b) => Math.round(b.getBoundingClientRect().left))).size,
    };
  });
  const at = c.name;
  if (m.cells < 30) bad("matrix-phone", at, `${m.cells} track toggles found, expected the sports grid's 30`);
  for (const o of m.over) bad("matrix-phone", at, `inside #matrix: ${o}`);
  if (m.doc > m.vw) bad("matrix-phone", at, `the page scrolls sideways: ${m.doc} > ${m.vw}`);
  if (!m.scrollW) bad("matrix-phone", at, "no .mx-scroll frame");
  else if (m.scrollW[0] > m.scrollW[1]) bad("matrix-phone", at, `the matrix frame scrolls sideways (scrollWidth ${m.scrollW[0]} > clientWidth ${m.scrollW[1]})`);
  if (m.hidden) bad("matrix-phone", at, `${m.hidden} track toggles are not visible`);
  if (m.short) bad("matrix-phone", at, `${m.short} track toggles are under 44px tall`);
  if (m.tnHidden.length) bad("matrix-phone", at, `the visible track name (.mx-tn) is not shown on ${m.tnHidden.length} of ${m.cells} toggles (${m.tnHidden.slice(0, 3).join(", ")})`);
  if (m.named !== m.cells) bad("matrix-phone", at, `only ${m.named} of ${m.cells} toggles are labelled "<their track> to <their row's destination>"`);
  for (const t of m.small) bad("matrix-phone", at, `text under 12px in the matrix: ${t}`);
  // A broken layout is already reported above: do not also wait a minute for a control that is not there.
  if (fail.some((f) => f.startsWith(`matrix-phone [${at}]`))) return;
  // It works: send a track to a destination, and the "hears:" line and the summary change. Two clicks on one
  // row make it the same mix as the row above, so the count of different mixes changes too.
  const state = () =>
    page.evaluate(() => ({
      hears: document.querySelector('#matrix [data-hears="1"]')?.textContent ?? "",
      sum: document.querySelector("#matrix [data-mx-summary]")?.textContent ?? "",
    }));
  const before = await state();
  const first = page.locator('#matrix button.mx-cell[data-d="1"][data-t="0"]');
  const second = page.locator('#matrix button.mx-cell[data-d="1"][data-t="1"]');
  const was = await first.getAttribute("aria-pressed");
  await first.click();
  await waitFn(page, ([w]) => document.querySelector('#matrix button.mx-cell[data-d="1"][data-t="0"]')?.getAttribute("aria-pressed") !== w, [was]);
  await second.click();
  await waitFn(page, ([h]) => document.querySelector('#matrix [data-hears="1"]')?.textContent !== h, [before.hears]);
  const after = await state();
  if (after.hears === before.hears) bad("matrix-phone", at, `the "hears:" line did not change after two clicks ("${after.hears}")`);
  if (after.sum === before.sum) bad("matrix-phone", at, `the summary did not change ("${after.sum}")`);
  // Keyboard: Space on a focused toggle flips it.
  await second.focus();
  const p0 = await second.getAttribute("aria-pressed");
  await page.keyboard.press("Space");
  await waitFn(page, ([w]) => document.querySelector('#matrix button.mx-cell[data-d="1"][data-t="1"]')?.getAttribute("aria-pressed") !== w, [p0]);
  // A preset swaps the whole grid and it still fits.
  await page.click('#matrix [data-preset="worship"]');
  await waitFn(page, () => document.querySelector('#matrix button.mx-cell[aria-label="Room mix to Custom RTMP"]') !== null);
  const doc2 = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  if (doc2 > 0) bad("matrix-phone", at, `after the Church preset the page scrolls sideways by ${doc2}px`);
  log.push(`matrix-phone [${at}] ${m.cells} toggles, ${m.cols} columns, frame ${m.scrollW?.join("/")}, hears/summary/Space/preset work`);
}

/** The journey bar's five stages: all fully inside the viewport at 390 and 360; where they cannot all fit (320)
 *  the bar scrolls, and each stage's link is fully in view once that stage is active. */
async function checkJourneyBar(page, c) {
  const read = () =>
    page.evaluate(() => {
      const bar = document.querySelector("[data-journey]");
      const ol = bar.querySelector("ol");
      const ob = ol.getBoundingClientRect();
      return {
        vw: window.innerWidth,
        h: Math.round(bar.getBoundingClientRect().height),
        token: parseFloat(getComputedStyle(document.documentElement).getPropertyValue("--journey-h")),
        fit: [ol.scrollWidth, ol.clientWidth],
        items: [...ol.querySelectorAll("a")].map((a) => {
          const r = a.getBoundingClientRect();
          return { name: a.textContent.trim(), left: r.left, right: r.right, current: a.getAttribute("aria-current") === "true", inOl: r.left >= ob.left - 0.5 && r.right <= ob.right + 0.5 };
        }),
      };
    });
  await page.evaluate(() => window.scrollTo({ top: 0, behavior: "instant" }));
  const m = await read();
  if (m.items.length !== 5) bad("journey-bar", c.name, `${m.items.length} journey items, expected five`);
  if (Math.abs(m.h - m.token) > 0.5) bad("journey-bar", c.name, `the bar is ${m.h}px tall but --journey-h says ${m.token}px`);
  if (c.width >= 360) {
    for (const i of m.items) if (i.left < -0.5 || i.right > m.vw + 0.5) bad("journey-bar", c.name, `"${i.name}" spans ${i.left.toFixed(0)}..${i.right.toFixed(0)} in a ${m.vw}px viewport`);
    if (m.fit[0] > m.fit[1]) bad("journey-bar", c.name, `the bar needs ${m.fit[0]}px of ${m.fit[1]}px: its items do not all fit`);
    log.push(`journey-bar [${c.name}] five items fit (${m.fit[0]} of ${m.fit[1]}px), last ends at ${m.items.at(-1)?.right.toFixed(0)}`);
    return;
  }
  // Narrower: it scrolls. Each stage, once active, must be fully in view (the bar brings it).
  const stages = ["how", "matrix", "why", "install", "use"];
  for (const [i, id] of stages.entries()) {
    await page.evaluate((id) => {
      const el = document.getElementById(id);
      const gap = parseFloat(getComputedStyle(el).scrollMarginTop) || 0;
      window.scrollTo({ top: Math.ceil(el.getBoundingClientRect().top + window.scrollY - gap), behavior: "instant" });
    }, id);
    try {
      await waitFn(
        page,
        ([i]) => {
          const links = [...document.querySelectorAll("[data-journey] ol a")];
          const a = links[i];
          const ob = a.closest("ol").getBoundingClientRect();
          const r = a.getBoundingClientRect();
          return a.getAttribute("aria-current") === "true" && r.left >= ob.left - 0.5 && r.right <= ob.right + 0.5 && r.left >= -0.5 && r.right <= window.innerWidth + 0.5;
        },
        [i],
      );
    } catch {
      const now = await read();
      bad("journey-bar", c.name, `with "${stages[i]}" active its link is not fully in view: ${JSON.stringify(now.items[i])}`);
    }
  }
  log.push(`journey-bar [${c.name}] scrolls (${m.fit[0]} in ${m.fit[1]}px); every stage's link was fully in view once active`);
}

/* ----------------------------------------------------- deck-stability */
const DECK_WIDTHS = [
  // 560: the 521-599px band, which has no legend floor of its own.
  { name: "560 deck", width: 560, height: 800, js: true, reduced: false, phone: true },
  { name: "430 deck", width: 430, height: 800, js: true, reduced: false, phone: true },
  { name: "390 deck", width: 390, height: 800, js: true, reduced: false, phone: true },
  { name: "360 deck", width: 360, height: 800, js: true, reduced: false, phone: true },
  { name: "320 deck", width: 320, height: 800, js: true, reduced: false, phone: true },
];
/** Over a full scripted cycle of the Stream Deck show (26 s, plus lead-in), nothing below its log moves. */
async function checkDeckStability(browser, origin, c) {
  const { ctx, page } = await open(browser, c);
  try {
    await load(page, origin, c);
    const gl = await mustGL(page, c.name, "deck-stability");
    await page.evaluate(() => document.querySelector("#deck").scrollIntoView({ behavior: "instant", block: "start" }));
    // The show only runs over the 3D device, so the canvas has to be up for the log to cycle.
    if (gl) await page.waitForSelector("[data-deck-box] canvas");
    await page.evaluate(() => {
      const top = (s) => document.querySelector(s).getBoundingClientRect().top + window.scrollY;
      const w = (window.__deck = { acts: [], foot: [], rows: new Set(), t0: performance.now() });
      w.timer = setInterval(() => {
        w.acts.push(top("#deck .dk-acts"));
        w.foot.push(top("#deck .dk-foot"));
        w.rows.add(document.querySelectorAll("#deck .dk-log li").length);
      }, 50);
    });
    await page.waitForTimeout(32_000); // a full 26 s show, plus the start
    const r = await page.evaluate(() => {
      const w = window.__deck;
      clearInterval(w.timer);
      const span = (a) => Math.max(...a) - Math.min(...a);
      return { acts: span(w.acts), foot: span(w.foot), rows: [...w.rows].sort(), samples: w.acts.length, ol: Math.round(document.querySelector("#deck .dk-log ol").getBoundingClientRect().height) };
    });
    log.push(`deck-stability [${c.name}] over ${r.samples} samples of one show cycle (log rows seen: ${r.rows.join(",")}; log ${r.ol}px): legend top moved ${r.acts}px, notes top moved ${r.foot}px`);
    if (r.rows.length < 4) bad("deck-stability", c.name, `the show's log never cycled (rows seen: ${r.rows.join(",")}); nothing was proved`);
    if (r.acts > 0.5) bad("deck-stability", c.name, `the legend moved ${r.acts}px while the log filled and emptied`);
    if (r.foot > 0.5) bad("deck-stability", c.name, `the Stream Deck notes moved ${r.foot}px while the log filled and emptied`);
  } finally {
    await ctx.close();
  }
}

/* ---------------------------------------------------------- stability */
async function checkStability(browser, origin, c) {
  const { ctx, page } = await open(browser, c);
  try {
    await page.addInitScript(() => {
      window.__shift = 0;
      window.__lcp = null;
      window.__shifts = [];
      new PerformanceObserver((l) => {
        for (const e of l.getEntries()) {
          if (e.hadRecentInput) continue;
          window.__shift += e.value;
          window.__shifts.push(`${e.value.toFixed(4)} ${(e.sources ?? []).map((s) => s.node?.nodeName + "." + (s.node?.className ?? "").toString().slice(0, 24)).join(",")}`);
        }
      }).observe({ type: "layout-shift", buffered: true });
      new PerformanceObserver((l) => {
        for (const e of l.getEntries()) window.__lcp = { inHero: !!e.element?.closest?.("[data-hero]"), tag: e.element?.tagName ?? null, id: e.element?.className?.toString?.().slice(0, 40) ?? "" };
      }).observe({ type: "largest-contentful-paint", buffered: true });
    });
    await load(page, origin, c);
    if (await mustGL(page, c.name, "stability")) await page.waitForSelector("[data-globe] canvas");
    await rafs(page);
    // The LCP candidate is final once the page is settled and before anything is scrolled to.
    const lcp = await page.evaluate(() => window.__lcp);
    // Layout shift is counted through a scroll of the whole page, where lazy images arrive.
    const height = await page.evaluate(() => document.documentElement.scrollHeight);
    for (let y = 0; y < height; y += c.height * 0.8) {
      await page.evaluate((y) => window.scrollTo({ top: y, behavior: "instant" }), y);
      await rafs(page);
    }
    await page.waitForLoadState("networkidle");
    await rafs(page);
    const shift = await page.evaluate(() => window.__shift);
    for (const sh of await page.evaluate(() => window.__shifts)) log.push(`stability [${c.name}] shift ${sh}`);
    log.push(`stability [${c.name}] layout shift ${shift.toFixed(4)} over load and a full scroll, LCP <${lcp?.tag}> ${lcp?.inHero ? "in" : "NOT in"} [data-hero]`);
    if (shift > MAX_SHIFT) bad("stability", c.name, `cumulative layout shift ${shift.toFixed(3)} is over ${MAX_SHIFT}`);
    if (!lcp) bad("stability", c.name, "no largest-contentful-paint entry");
    else if (!lcp.inHero) bad("stability", c.name, `the LCP element (<${lcp.tag} class="${lcp.id}">) is not inside [data-hero]`);
  } finally {
    await ctx.close();
  }
}

/* ---------------------------------------------------------- phone fit */
/** Runs in the page: every rendered string under MIN px, and every long name that overflows. */
function phoneProblems([min, names]) {
  const out = { small: [], overflow: [], seen: 0 };
  const skip = new Set(["SCRIPT", "STYLE", "NOSCRIPT", "TITLE", "TEMPLATE"]);
  const describe = (el) => `${el.tagName.toLowerCase()}${el.id ? "#" + el.id : ""}${typeof el.className === "string" && el.className ? "." + el.className.trim().split(/\s+/)[0] : ""}`;
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const text = n.data.replace(/\s+/g, " ").trim();
    if (!text) continue;
    const el = n.parentElement;
    if (!el || skip.has(el.tagName) || el.closest("canvas")) continue;
    const rects = el.getClientRects();
    if (!rects.length) continue; // display:none, closed <details>, a hidden panel
    const r = el.getBoundingClientRect();
    if (r.width <= 1 || r.height <= 1) continue; // sr-only
    const cs = getComputedStyle(el);
    if (cs.visibility === "hidden") continue;
    out.seen++;
    let px = parseFloat(cs.fontSize);
    const svg = el.closest("svg");
    if (svg && el instanceof SVGElement) {
      const m = el.getScreenCTM();
      if (m) px *= Math.hypot(m.a, m.b);
    }
    if (px < min - 0.01) out.small.push(`${describe(el)} "${text.slice(0, 30)}" ${px.toFixed(2)}px`);
    for (const name of names) {
      if (!n.data.includes(name)) continue;
      // The range over the name itself, against every box that is meant to hold it.
      const i = n.data.indexOf(name);
      const range = document.createRange();
      range.setStart(n, i);
      range.setEnd(n, i + name.length);
      const t = range.getBoundingClientRect();
      let box = el;
      while (box && getComputedStyle(box).display === "inline") box = box.parentElement;
      const boxes = [box, box?.parentElement].filter(Boolean);
      for (const b of boxes) {
        const br = b.getBoundingClientRect();
        if (t.right > br.right + 1 || t.left < br.left - 1) out.overflow.push(`"${name}" in ${describe(b)} spans ${t.left.toFixed(0)}..${t.right.toFixed(0)}, its box ${br.left.toFixed(0)}..${br.right.toFixed(0)}`);
      }
      if (box && box.scrollWidth > box.clientWidth + 1 && getComputedStyle(box).overflowX !== "visible") out.overflow.push(`"${name}" in ${describe(box)}: scrollWidth ${box.scrollWidth} > clientWidth ${box.clientWidth}`);
      if (t.right > window.innerWidth + 0.5 || t.left < -0.5) out.overflow.push(`"${name}" in ${describe(el)} leaves the viewport: ${t.left.toFixed(0)}..${t.right.toFixed(0)}`);
      out.names = (out.names ?? 0) + 1;
    }
  }
  return out;
}

async function checkPhoneFit(page, c) {
  const run = () => page.evaluate(phoneProblems, [MIN_TEXT_PX, LONG_NAMES]);
  const states = [];
  states.push(["as loaded", await run()]);
  // Every example and every matrix preset, so the other examples' names are on screen too.
  for (const key of CASE_ORDER) {
    await page.click(`#tab-${key}`);
    await waitFn(page, (k) => !document.querySelector(`#panel-${k}`)?.hidden, key);
    await page.click(`#matrix [data-preset="${key}"]`);
    await rafs(page);
    states.push([`example ${key}`, await run()]);
  }
  // Then everything that is hidden until asked for: every tab panel and every <details>, opened.
  await page.evaluate(() => {
    for (const p of document.querySelectorAll("[role=tabpanel]")) p.hidden = false;
    for (const d of document.querySelectorAll("details")) d.open = true;
  });
  states.push(["everything opened", await run()]);
  const small = new Set();
  const over = new Set();
  let names = 0;
  let seen = 0;
  for (const [, r] of states) {
    r.small.forEach((s) => small.add(s));
    r.overflow.forEach((s) => over.add(s));
    names = Math.max(names, r.names ?? 0);
    seen = Math.max(seen, r.seen);
  }
  log.push(`phone-fit [${c.name}] ${seen} text runs measured, ${names} long-name occurrences measured (${LONG_NAMES.join(" / ")})`);
  if (names === 0) bad("phone-fit", c.name, `no occurrence of ${LONG_NAMES.join(" or ")} was found on the page to measure`);
  if (seen < 200) bad("phone-fit", c.name, `only ${seen} text runs were measured; the walk is not reading the page`);
  for (const s of [...small].slice(0, 25)) bad("phone-fit", c.name, `text under ${MIN_TEXT_PX}px: ${s}`);
  if (small.size > 25) bad("phone-fit", c.name, `... and ${small.size - 25} more under ${MIN_TEXT_PX}px`);
  for (const s of [...over].slice(0, 25)) bad("phone-fit", c.name, `overflow: ${s}`);
}

/* ------------------------------------------------------------- length */
async function checkLength(page, c) {
  // The hero is taller or shorter by example, so the page is measured in each and the tallest counts.
  // Folds are left as the page shipped them (closed).
  let tallest = { h: 0, key: "" };
  let sections = [];
  for (const key of CASE_ORDER) {
    await page.click(`#tab-${key}`);
    await waitFn(page, (k) => !document.querySelector(`#panel-${k}`)?.hidden, key);
    await rafs(page);
    const m = await page.evaluate(
      (secs) => ({
        h: document.documentElement.scrollHeight,
        sections: secs.map((s) => [s, Math.round(document.querySelector(s)?.getBoundingClientRect().height ?? -1)]),
      }),
      SECTIONS,
    );
    if (m.h > tallest.h) {
      tallest = { h: m.h, key };
      sections = m.sections;
    }
  }
  log.push(`length [${c.name}] page ${tallest.h}px with the "${tallest.key}" example (limit ${MAX_HEIGHT_390}); ${sections.map(([s, h]) => `${s} ${h}`).join(", ")}`);
  if (tallest.h > MAX_HEIGHT_390) {
    const big = sections.reduce((a, b) => (b[1] > a[1] ? b : a));
    bad("length", c.name, `the page is ${tallest.h}px tall at ${c.width} wide with the "${tallest.key}" example, over the limit of ${MAX_HEIGHT_390}px; the tallest section is ${big[0]} at ${big[1]}px. Secondary content collapses behind disclosure controls before primary content does.`);
  }
}

/* --------------------------------------------------------- disclosure */
/** The blocks the page collapses behind <details data-collapse> at phone width only. */
async function checkDisclosure(page, c) {
  const m = await page.evaluate(() => {
    const visible = (el) => !!el && getComputedStyle(el).visibility !== "hidden" && el.getClientRects().length > 0;
    return [...document.querySelectorAll("details[data-collapse]")].map((d) => {
      const summary = d.querySelector(":scope > summary");
      const body = [...d.children].find((e) => e !== summary);
      const dr = d.getBoundingClientRect().height;
      return {
        open: d.open,
        summary: summary?.textContent.replace(/\s+/g, " ").trim() ?? null,
        summaryShown: visible(summary),
        bodyText: (body?.textContent ?? "").trim().length,
        bodyShown: visible(body) && body.getBoundingClientRect().height > 0,
        // A closed <details> is as tall as its summary: the content has no height of its own.
        collapsed: dr <= (summary?.getBoundingClientRect().height ?? 0) + 1,
      };
    });
  });
  if (m.length === 0) throw new Error("no details[data-collapse] on the page");
  log.push(`disclosure [${c.name}] ${m.length} collapsible blocks: ${m.map((x) => x.summary).join(" | ")}`);
  for (const [i, d] of m.entries()) {
    const at = `${c.name} block ${i + 1} ("${d.summary}")`;
    if (!d.summary) bad("disclosure", at, "has no <summary>");
    if (d.bodyText === 0) bad("disclosure", at, "has no content in the server-rendered HTML");
    if (c.phone) {
      if (d.open) bad("disclosure", at, "starts open on a phone");
      if (!d.summaryShown) bad("disclosure", at, "its summary is not rendered on a phone");
      if (!d.collapsed) bad("disclosure", at, "its content takes up room while the block is closed");
    } else {
      if (d.summaryShown) bad("disclosure", at, "a disclosure control is visible on desktop");
      if (!d.bodyShown) bad("disclosure", at, "its content is not shown on desktop");
    }
  }
  if (!c.phone) return;
  // Keyboard: Enter on the summary opens it, and the focus ring is drawn.
  const first = page.locator("details[data-collapse] > summary").first();
  await first.scrollIntoViewIfNeeded();
  // Reach it the way a keyboard user does: Tab from the element just before it, so :focus-visible applies.
  await first.evaluate((sm) => {
    const prev = document.createElement("span");
    prev.tabIndex = 0;
    sm.closest("details").before(prev);
    prev.focus();
    prev.dataset.probe = "";
  });
  await page.keyboard.press("Tab");
  if (!(await first.evaluate((sm) => document.activeElement === sm))) bad("disclosure", c.name, "Tab from the element before the first fold did not land on its summary");
  await page.evaluate(() => document.querySelector("[data-probe]")?.remove());
  const ring = await first.evaluate((s) => {
    const cs = getComputedStyle(s);
    return { style: cs.outlineStyle, width: parseFloat(cs.outlineWidth) };
  });
  if (ring.style === "none" || ring.width < 1) bad("disclosure", c.name, `a focused summary has no visible focus ring (outline ${ring.style} ${ring.width}px)`);
  await page.keyboard.press("Enter");
  await waitFn(page, () => document.querySelector("details[data-collapse]")?.open === true);
  const shown = await page.evaluate(() => {
    const d = document.querySelector("details[data-collapse]");
    return d.getBoundingClientRect().height > d.querySelector("summary").getBoundingClientRect().height + 10;
  });
  if (!shown) bad("disclosure", c.name, "Enter opened the first block but its content has no height");
  await page.keyboard.press("Enter");
  await waitFn(page, () => document.querySelector("details[data-collapse]")?.open === false);
}

/* ---------------------------------------------------------------- run */
const t00 = Date.now();
const srv = await serve();
const origin = `http://127.0.0.1:${srv.address().port}`;
const browsers = [];
const deadlineTimer = setTimeout(async () => {
  stopped = true;
  summarize(true);
  // Closing can hang on a wedged browser: give it five seconds, then leave regardless.
  await Promise.race([Promise.all(browsers.map((b) => b.close().catch(() => {}))), new Promise((r) => setTimeout(r, 5000))]);
  process.exit(1);
}, DEADLINE_S * 1000);
const launch = async (args = []) => {
  const b = await chromium.launch({ args });
  browsers.push(b);
  return b;
};
try {
  // Every browser that is supposed to have WebGL gets the flags a GPU-less runner needs, so the answer is the
  // same on a laptop and in CI. CHECK_HOME_NO_GL=1 takes WebGL away from them (the proof that this fails loudly).
  const browser = await launch(process.env.CHECK_HOME_NO_GL ? NO_GL_ARGS : GL_ARGS);
  const wide = CONTEXTS[0];
  const sw = await launch(process.env.CHECK_HOME_NO_GL ? NO_GL_ARGS : GL_ARGS);
  const noGl = await launch(NO_GL_ARGS);

  const runContext = async (c) => {
    const t0 = Date.now();
    const { ctx, page } = await open(browser, c);
    try {
      try {
        await load(page, origin, c);
      } catch (e) {
        bad("first-screen", c.name, `the page did not load or boot: ${String(e?.message ?? e).split("\n")[0]}`);
        return;
      }
      if (!c.js) {
        await check("first-screen", c.name, () => checkFirstScreen(page, c));
        await check("no-script", c.name, () => checkNoScript(page, c));
      } else if (c.reduced) {
        await check("still", c.name, () => checkStill(page, c, "reduced motion"));
      } else {
        await check("first-screen", c.name, () => checkFirstScreen(page, c));
        await check("contrast", c.name, () => checkContrast(page, c));
        if (c.phone) {
          await check("length", c.name, () => checkLength(page, c));
          await check("keyboard", c.name, async () => {
            await checkHeroTabOrder(page, c);
            await checkScriptControls(page, c);
          });
        } else {
          await check("keyboard", c.name, () => checkKeyboard(page, c));
        }
        await check("disclosure", c.name, () => checkDisclosure(page, c));
        await check("console", c.name, () => checkConsole(page, c));
        // Last: it opens every fold and un-hides every tab panel to measure what is behind them.
        if (c.phone) await check("phone-fit", c.name, () => checkPhoneFit(page, c));
      }
    } finally {
      await ctx.close();
      durations.push(`${c.name} ${((Date.now() - t0) / 1000).toFixed(1)}s`);
    }
  };
  // Pause gets a page of its own (the longest wait of all, so it runs beside the other checks, not after them).
  const runPause = async (c) => {
    const t0 = Date.now();
    const { ctx, page } = await open(browser, c);
    try {
      try {
        await load(page, origin, c);
      } catch (e) {
        bad("still", `${c.name} pause`, `the page did not load or boot: ${String(e?.message ?? e).split("\n")[0]}`);
        return;
      }
      await check("still", `${c.name} pause`, () => checkPause(page, c));
    } finally {
      await ctx.close();
      durations.push(`${c.name} pause ${((Date.now() - t0) / 1000).toFixed(1)}s`);
    }
  };
  const runLazy = async () => {
    const t0 = Date.now();
    await check("lazy-3d", wide.name, () => checkLazy3d(sw, origin, wide));
    await check("lazy-3d", `${wide.name} no-webgl`, () => checkNoWebGL(noGl, origin, wide));
    await check("model-fail", `${wide.name} model-fail`, () => checkModelFails(sw, origin, wide));
    durations.push(`lazy-3d browsers ${((Date.now() - t0) / 1000).toFixed(1)}s`);
  };
  const runPhone = async (c) => {
    const t0 = Date.now();
    const { ctx, page } = await open(browser, c);
    try {
      try {
        await load(page, origin, c);
      } catch (e) {
        bad("matrix-phone", c.name, `the page did not load or boot: ${String(e?.message ?? e).split("\n")[0]}`);
        return;
      }
      await check("matrix-phone", c.name, () => checkMatrixPhone(page, c));
      await check("journey-bar", c.name, () => checkJourneyBar(page, c));
    } finally {
      await ctx.close();
      durations.push(`${c.name} ${((Date.now() - t0) / 1000).toFixed(1)}s`);
    }
  };
  const runDeck = async (c) => {
    const t0 = Date.now();
    await check("deck-stability", c.name, () => checkDeckStability(browser, origin, c));
    durations.push(`${c.name} ${((Date.now() - t0) / 1000).toFixed(1)}s`);
  };

  // The units of work are independent pages, so they run side by side so the long waits overlap, but never
  // more at once than a small runner can draw WebGL for: one more than the CPUs (a unit is partly waiting, but software WebGL starved every wait when nine ran
  // together on 2 vCPUs, and measured best at 3 there). Slowest first. CHECK_HOME_CONCURRENCY overrides.
  const heavy = (c) => CONTEXTS.find((x) => x.name === c);
  const tasks = [
    () => runPause(heavy("390 js")),
    () => runContext(heavy("390 js")),
    () => runPause(heavy("1440 js")),
    () => runContext(heavy("1440 js")),
    () => runContext(heavy("390 reduced")),
    () => runContext(heavy("1440 reduced")),
    ...DECK_WIDTHS.map((c) => () => runDeck(c)),
    ...PHONES.map((c) => () => runPhone(c)),
    () => runLazy(),
    () => runContext(heavy("1440 no-js")),
    () => runContext(heavy("390 no-js")),
  ];
  const width = Number(process.env.CHECK_HOME_CONCURRENCY) || Math.max(3, availableParallelism() + 1);
  const queue = [...tasks];
  console.log(`running ${tasks.length} units of work, ${width} at a time (${availableParallelism()} CPUs)`);
  await Promise.all(
    Array.from({ length: width }, async () => {
      while (queue.length && !stopped) await queue.shift()();
    }),
  );
  // Alone: layout shift is read off a load, so nothing else may be competing for the CPU while it runs.
  const phone = CONTEXTS.find((c) => c.phone && c.js && !c.reduced);
  await check("stability", phone.name, () => checkStability(browser, origin, phone));
} finally {
  // Never hang here: a wedged browser gets ten seconds to close, then the process leaves regardless.
  await Promise.race([Promise.all(browsers.map((b) => b.close().catch(() => {}))), new Promise((r) => setTimeout(r, 10_000))]);
  srv.closeAllConnections?.();
  srv.close();
}

/** The summary: coverage, what ran where, durations, every failure. `early` = the deadline fired. */
function summarize(early) {
  if (!early) {
    for (const [id, ctxs] of Object.entries(EXPECTED)) {
      if (ONLY && !ONLY.includes(id)) continue;
      const missing = ctxs.filter((x) => !ran.has(`${id}|${x}`));
      if (missing.length) bad("coverage", id, `did not run in: ${missing.join(", ")}`);
    }
  }
  const byCtx = new Map();
  for (const key of ran) {
    const [id, ctx] = key.split("|");
    byCtx.set(ctx, [...(byCtx.get(ctx) ?? []), id]);
  }
  for (const [ctx, ids] of [...byCtx].sort()) log.push(`ran [${ctx}]: ${ids.join(", ")}`);
  log.sort();
  for (const l of log) console.log(l);
  console.log(`timings: ${timings.join("; ")}`);
  console.log(`durations (each context, in parallel): ${durations.sort().join("; ")}; total ${((Date.now() - T00) / 1000).toFixed(1)}s`);
  if (early) console.error(`DEADLINE: ${DEADLINE_S}s passed. Still running: ${[...running.values()].join("; ") || "nothing"}.`);
  if (fail.length) {
    const seen = new Set();
    const uniq = fail.filter((f) => !seen.has(f) && seen.add(f));
    console.error(`home page: ${uniq.length} problem(s)${early ? " (run STOPPED at the deadline)" : ""}\n  ${uniq.join("\n  ")}`);
  }
}

if (stopped) process.exit(1);
clearTimeout(deadlineTimer);
summarize(false);
if (fail.length) process.exit(1);
if (ONLY) {
  const skipped = Object.keys(EXPECTED).filter((id) => !ONLY.includes(id));
  console.log(`PARTIAL RUN, NOT A PASS: only ${ONLY.join(", ")} ran (CHECK_HOME_ONLY). Skipped: ${skipped.join(", ")}. No failures in what ran.`);
} else {
  console.log(`home page passed (${CONTEXTS.length} contexts, ${ran.size} check runs)`);
}
