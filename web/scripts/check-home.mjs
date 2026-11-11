#!/usr/bin/env node
/* The home page, measured in a real browser against the BUILT site.
 *
 * check-build.mjs reads dist/ as text and the unit tests run in jsdom, so
 * neither can prove what the homepage's design depends on: that the headline,
 * the primary action and the diagram share the first screen of a phone, that
 * the diagram's lines end under their cards, that the pinned "how it works"
 * stage pins, that nothing needs script to read, or that a Spanish label does
 * not break a card. Those are layout and behaviour, so this loads / in Chromium.
 *
 * CONTEXTS. 1440x900 and 390x844 (a touch phone), each three ways: script on,
 * script off, and script on with prefers-reduced-motion. Assertion letters
 * (A..K) are the ones named in the homepage plan, Task 11; every failure is
 * printed with its letter and context, and the run fails with the whole list.
 *
 *   A first screen: h1, the Install button and the diagram fit
 *   B console: no console error and no uncaught exception, in any context
 *   C keyboard: tabs (arrows, Home, End) and a destination (Enter, Escape)
 *   D reduced motion: nothing animates, the counter and the failover settle,
 *     and Pull the cable is one instant change to the slate
 *   E Pause motion switches motion off
 *   F no script: everything script reveals stays hidden (failover controls
 *     too), the markup stands
 *   G a long Spanish label does not overflow a card (390, every story)
 *   H the pinned stage sticks under the nav and lights step 3 at 40%
 *   I every line ends inside the card it points at, and none hides behind
 *     another card on the way
 *   J contrast of the headline and the lede on the page ground
 *   K layout shift and the LCP element on a phone
 *   L the failover autoplays on a short phone (375x667, its own context)
 *
 * GREEN OVER NOTHING. Every assertion reads an element it first requires, and
 * a missing element is a failure of its own letter, never a skipped check. A
 * run against a build that is not the new homepage fails A and F.
 *
 * Needs a Chromium: `npx playwright install chromium` once. Run after
 * `npm run build`. CHECK_HOME_DIST points at another dist/ (the RED proof
 * runs it against main's).
 */
import { createServer } from "node:http";
import { createReadStream, existsSync, statSync } from "node:fs";
import { extname, join, resolve, sep } from "node:path";
import { chromium } from "playwright";

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
  ".ico": "image/x-icon",
};
const STORIES = ["streamer", "sports", "worship"];
// The headline is held to the home palette's pin; the lede to the same bar
// the brief sets for every home paragraph.
const MIN_CONTRAST = 7;
const MAX_SHIFT = 0.1;

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

const fail = [];
const log = [];
const bad = (id, ctx, msg) => fail.push(`${id} [${ctx}] ${msg}`);

/** Run one lettered check; a throw (a missing element, a timeout) is a failure of that letter. */
async function check(id, ctx, fn) {
  try {
    await fn();
  } catch (e) {
    bad(id, ctx, `threw: ${String(e?.message ?? e).split("\n")[0]}`);
  }
}

const CONTEXTS = [
  { name: "1440 js", width: 1440, height: 900, js: true, reduced: false },
  { name: "390 js", width: 390, height: 844, js: true, reduced: false, phone: true },
  { name: "1440 no-js", width: 1440, height: 900, js: false, reduced: false },
  { name: "390 no-js", width: 390, height: 844, js: false, reduced: false, phone: true },
  { name: "1440 reduced", width: 1440, height: 900, js: true, reduced: true },
  { name: "390 reduced", width: 390, height: 844, js: true, reduced: true, phone: true },
];

/** After load: wait for the page's script to have booted (the tablist un-hidden),
 * then for html.motion-on or 2.5s, whichever comes first. */
async function settle(page) {
  await page.waitForFunction(() => document.querySelector("[data-story-tabs]")?.hidden === false, null, { timeout: 8000 });
  await page
    .waitForFunction(() => document.documentElement.classList.contains("motion-on"), null, { timeout: 2500 })
    .catch(() => {});
}

/** The visible hero panel's diagram. */
const HERO_DG = "[data-hero] .panel:not([hidden]) .dg";

/* ---------------------------------------------------------------- A */
async function checkA(page, c) {
  const m = await page.evaluate((sel) => {
    const vh = window.innerHeight;
    const bottom = (s) => document.querySelector(s)?.getBoundingClientRect().bottom ?? null;
    const dg = document.querySelector(sel)?.getBoundingClientRect();
    const dest = document.querySelector(`${sel} .dest`)?.getBoundingClientRect();
    return {
      vh,
      h1: bottom("h1"),
      cta: bottom('a[href="#run"].btn-onair'),
      dgTop: dg?.top ?? null,
      dgBottom: dg?.bottom ?? null,
      destTop: dest?.top ?? null,
      sw: document.documentElement.scrollWidth,
      iw: window.innerWidth,
    };
  }, HERO_DG);
  log.push(`A [${c.name}] h1 bottom ${m.h1?.toFixed(0)}, cta bottom ${m.cta?.toFixed(0)}, dg ${m.dgTop?.toFixed(0)}..${m.dgBottom?.toFixed(0)}, first dest top ${m.destTop?.toFixed(0)} (vh ${m.vh})`);
  if (m.h1 === null) bad("A", c.name, "no h1");
  else if (m.h1 > m.vh) bad("A", c.name, `h1 bottom ${m.h1.toFixed(0)} is below the fold (${m.vh})`);
  if (m.cta === null) bad("A", c.name, 'no a[href="#run"].btn-onair');
  else if (m.cta > m.vh) bad("A", c.name, `the Install button's bottom ${m.cta.toFixed(0)} is below the fold (${m.vh})`);
  if (m.dgTop === null) bad("A", c.name, "no visible .dg in the hero");
  else if (c.phone) {
    if (m.dgTop > m.vh - 160) bad("A", c.name, `diagram top ${m.dgTop.toFixed(0)} leaves under 160px above the fold (${m.vh - 160} max)`);
    if (m.destTop === null || m.destTop >= m.vh) bad("A", c.name, `first .dest top ${m.destTop?.toFixed(0)} is not above the fold (${m.vh})`);
  } else if (m.dgBottom > m.vh) {
    bad("A", c.name, `diagram bottom ${m.dgBottom.toFixed(0)} is below the fold (${m.vh})`);
  }
  if (m.sw > m.iw) bad("A", c.name, `the page scrolls sideways: scrollWidth ${m.sw} > ${m.iw}`);
}

/* ---------------------------------------------------------------- C */
async function checkC(page, c) {
  const state = () =>
    page.evaluate(() => ({
      focus: document.activeElement?.id ?? "",
      selected: [...document.querySelectorAll('[role=tab][aria-selected="true"]')].map((t) => t.id),
      sportsVisible: !!document.querySelector("#panel-sports")?.getClientRects().length && !document.querySelector("#panel-sports").hidden,
      worshipVisible: !!document.querySelector("#panel-worship")?.getClientRects().length && !document.querySelector("#panel-worship").hidden,
      streamerVisible: !!document.querySelector("#panel-streamer")?.getClientRects().length && !document.querySelector("#panel-streamer").hidden,
    }));
  await page.focus("#tab-streamer");
  await page.keyboard.press("ArrowRight");
  await page.waitForFunction(() => document.querySelector("#tab-sports")?.getAttribute("aria-selected") === "true");
  await page.waitForFunction(() => !document.querySelector("#panel-sports")?.hidden && document.querySelector("#panel-streamer")?.hidden);
  let s = await state();
  if (s.focus !== "tab-sports") bad("C", c.name, `ArrowRight left focus on "${s.focus}", not #tab-sports`);
  if (!s.sportsVisible) bad("C", c.name, "ArrowRight did not show #panel-sports");
  await page.keyboard.press("End");
  await page.waitForFunction(() => document.querySelector("#tab-worship")?.getAttribute("aria-selected") === "true");
  s = await state();
  if (s.focus !== "tab-worship") bad("C", c.name, `End left focus on "${s.focus}", not #tab-worship`);
  await page.keyboard.press("Home");
  await page.waitForFunction(() => document.querySelector("#tab-streamer")?.getAttribute("aria-selected") === "true");
  await page.waitForFunction(() => !document.querySelector("#panel-streamer")?.hidden);
  s = await state();
  if (s.focus !== "tab-streamer") bad("C", c.name, `Home left focus on "${s.focus}", not #tab-streamer`);
  if (!s.streamerVisible) bad("C", c.name, "Home did not show #panel-streamer");

  const first = page.locator("#panel-streamer .dest").first();
  // The caption with its hint, as the page's script writes it at rest.
  const rest = ((await page.textContent("[data-status]")) ?? "").trim();
  await first.focus();
  await page.keyboard.press("Enter");
  await page.waitForFunction(() => document.querySelector("#panel-streamer .dest")?.getAttribute("aria-pressed") === "true");
  const status = (await page.textContent("[data-status]")) ?? "";
  if (!status.includes("carries")) bad("C", c.name, `Enter on a destination: status "${status.trim()}" lacks "carries"`);
  await page.keyboard.press("Escape");
  await page.waitForFunction(() => document.querySelector("#panel-streamer .dest")?.getAttribute("aria-pressed") === "false");
  const after = ((await page.textContent("[data-status]")) ?? "").trim();
  if (rest === "" || after !== rest || after.includes("carries")) {
    bad("C", c.name, `Escape: status is "${after}", expected the caption and hint "${rest}"`);
  }
}

/* ---------------------------------------------------------------- D */
async function checkD(page, c) {
  const m = await page.evaluate(() => ({
    motionOn: document.documentElement.classList.contains("motion-on"),
    anim: getComputedStyle(document.querySelector(".ln-audio")).animationName,
  }));
  if (m.motionOn) bad("D", c.name, "html.motion-on is set under reduced motion");
  if (m.anim !== "none") bad("D", c.name, `.ln-audio animationName is "${m.anim}", expected "none"`);
  await page.waitForTimeout(4000);
  const after = await page.evaluate(() => ({
    pcount: document.querySelector("[data-pcount]")?.textContent?.trim() ?? null,
    state: document.querySelector("[data-failover] [data-frame]")?.dataset.state ?? null,
  }));
  if (after.pcount !== "3") bad("D", c.name, `[data-pcount] reads "${after.pcount}" after 4s, expected "3"`);
  if (after.state !== "live") bad("D", c.name, `failover frame data-state is "${after.state}" after 4s, expected "live"`);
  // With motion off, "Pull the cable" is one instant change to the slate, not a timed run.
  await page.locator("[data-failover] [data-play]").scrollIntoViewIfNeeded();
  await page.click("[data-failover] [data-play]");
  const frameState = () => page.evaluate(() => document.querySelector("[data-failover] [data-frame]")?.dataset.state ?? null);
  const now = await frameState();
  if (now !== "slate") bad("D", c.name, `Pull the cable under reduced motion shows "${now}", expected "slate" at once`);
  await page.waitForTimeout(2000);
  const later = await frameState();
  if (later !== "slate") bad("D", c.name, `Pull the cable under reduced motion moved on to "${later}" within 2s; nothing may be timed`);
}

/* ---------------------------------------------------------------- E */
async function checkE(page, c) {
  await page.click("[data-pause]");
  const m = await page.evaluate(() => {
    const b = document.querySelector("[data-pause]");
    return { pressed: b.getAttribute("aria-pressed"), text: b.textContent.trim(), motionOn: document.documentElement.classList.contains("motion-on") };
  });
  if (m.pressed !== "true") bad("E", c.name, `aria-pressed is "${m.pressed}" after the click`);
  if (m.text !== "Resume motion") bad("E", c.name, `Pause reads "${m.text}", expected "Resume motion"`);
  if (m.motionOn) bad("E", c.name, "html.motion-on is still set after Pause");
}

/* ---------------------------------------------------------------- F */
async function checkF(page, c) {
  const m = await page.evaluate(() => {
    const disp = (s) => {
      const el = document.querySelector(s);
      return el ? getComputedStyle(el).display : null;
    };
    const run = document.querySelector("#run");
    const details = run?.querySelector("details");
    if (details) details.open = true;
    return {
      tablist: disp("[role=tablist]"),
      pause: disp("[data-pause]"),
      sports: disp("#panel-sports"),
      worship: disp("#panel-worship"),
      dests: [...document.querySelectorAll(".dest")].map((d) => d.tagName),
      status: document.querySelector("[data-status]")?.textContent ?? null,
      steps: document.querySelectorAll(".step h3").length,
      pres: run ? run.querySelectorAll("pre").length : 0,
      motionOn: document.documentElement.classList.contains("motion-on"),
      // Rendered at all? (A hidden ancestor leaves the buttons' own display alone.)
      foControls: [...document.querySelectorAll("[data-failover] button[data-stage], [data-failover] [data-play]")].map((b) => b.getClientRects().length),
    };
  });
  for (const k of ["tablist", "pause", "sports", "worship"]) {
    if (m[k] === null) bad("F", c.name, `no ${k} element to measure`);
    else if (m[k] !== "none") bad("F", c.name, `${k} has computed display "${m[k]}", expected none`);
  }
  if (m.dests.length === 0) bad("F", c.name, "no .dest elements");
  else if (m.dests.some((t) => t !== "DIV")) bad("F", c.name, `.dest elements are ${[...new Set(m.dests)].join("/")}, expected DIV`);
  if (m.status === null) bad("F", c.name, "no [data-status]");
  else if (m.status.includes("Tap")) bad("F", c.name, `the status says "Tap" with no script: "${m.status.trim()}"`);
  if (m.steps !== 5) bad("F", c.name, `${m.steps} .step h3, expected 5`);
  if (m.pres !== 2) bad("F", c.name, `${m.pres} pre in #run, expected 2`);
  if (m.motionOn) bad("F", c.name, "html.motion-on is set with script off");
  if (m.foControls.length !== 5) bad("F", c.name, `${m.foControls.length} failover controls, expected 4 stage buttons and Pull the cable`);
  else if (m.foControls.some((n) => n > 0)) bad("F", c.name, "a failover control is rendered with script off, where it can do nothing");
}

/* ---------------------------------------------------------------- G and I */
/** Runs in the page. The geometry of every visible diagram's lines against its cards. */
function lineProblems() {
  const inside = (p, r, tol) => p.x >= r.left - tol && p.x <= r.right + tol && p.y >= r.top - tol && p.y <= r.bottom + tol;
  const out = [];
  let measured = 0;
  for (const dg of document.querySelectorAll(".dg")) {
    if (!dg.getClientRects().length) continue;
    for (const path of dg.querySelectorAll("path[data-to]")) {
      const svg = path.ownerSVGElement;
      if (!svg || getComputedStyle(svg).display === "none") continue;
      const m = path.getScreenCTM();
      const at = (len) => {
        const p = path.getPointAtLength(len);
        const q = new DOMPoint(p.x, p.y).matrixTransform(m);
        return { x: q.x, y: q.y };
      };
      const total = path.getTotalLength();
      const to = path.dataset.to;
      const from = path.dataset.from;
      const layout = svg.dataset.layout;
      const name = `${dg.dataset.story}/${layout} ${from}->${to}`;
      if (to === "hub") {
        const chip = dg.querySelector(`.si[data-src="${from}"]`);
        if (!chip) {
          out.push(`${name}: no .si chip for source ${from}`);
          continue;
        }
        measured++;
        const s = at(0);
        if (!inside(s, chip.getBoundingClientRect(), 4)) out.push(`${name}: starts at (${s.x.toFixed(0)},${s.y.toFixed(0)}), outside its chip`);
        continue;
      }
      const card = [...dg.querySelectorAll(".dest")].find((d) => d.dataset.dest === to);
      if (!card) {
        out.push(`${name}: no .dest card for ${to}`);
        continue;
      }
      measured++;
      const e = at(total);
      if (!inside(e, card.getBoundingClientRect(), 4)) out.push(`${name}: ends at (${e.x.toFixed(0)},${e.y.toFixed(0)}), outside its card`);
      // The cards sit above the lines, so a line that crosses another card on
      // the way is hidden there. Sample the path against every OTHER card.
      for (const other of dg.querySelectorAll(".dest")) {
        if (other === card) continue;
        const r = other.getBoundingClientRect();
        const shrunk = { left: r.left + 2, right: r.right - 2, top: r.top + 2, bottom: r.bottom - 2 };
        for (let i = 0; i <= 80; i++) {
          if (inside(at((total * i) / 80), shrunk, 0)) {
            out.push(`${name}: passes behind the ${other.dataset.dest} card`);
            break;
          }
        }
      }
    }
  }
  return { problems: out, measured };
}

async function checkGI(page, c) {
  let measured = 0;
  for (const story of STORIES) {
    await page.click(`#tab-${story}`);
    await page.waitForFunction((k) => !document.querySelector(`#panel-${k}`)?.hidden && document.querySelectorAll("[data-hero] .panel.swap-out").length === 0, story);
    if (c.phone) {
      // G: the longest label this site could be asked to hold, in Spanish.
      const g = await page.evaluate((k) => {
        const panel = document.querySelector(`#panel-${k}`);
        const name = panel.querySelector(".dest-name");
        const sub = panel.querySelector(".dest-sub");
        if (!name || !sub) return { missing: true };
        name.textContent = "Servicio en español para toda la familia";
        sub.textContent = "Comentario en español y música";
        return {
          cards: [...panel.querySelectorAll(".dest")].map((d) => d.scrollWidth - d.clientWidth),
          doc: document.documentElement.scrollWidth,
        };
      }, story);
      if (g.missing) bad("G", c.name, `${story}: no .dest-name / .dest-sub`);
      else {
        if (g.cards.length === 0) bad("G", c.name, `${story}: no .dest cards`);
        if (g.cards.some((d) => d > 1)) bad("G", c.name, `${story}: a card overflows by ${Math.max(...g.cards)}px with the Spanish label`);
        if (g.doc > 390) bad("G", c.name, `${story}: the page is ${g.doc}px wide with the Spanish label`);
      }
    } else {
      await page.evaluate((k) => {
        const panel = document.querySelector(`#panel-${k}`);
        panel.querySelector(".dest-name").textContent = "Servicio en español para toda la familia";
        panel.querySelector(".dest-sub").textContent = "Comentario en español y música";
      }, story);
    }
    // I: after the label change, so a taller card is measured as it ends up.
    await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
    const r = await page.evaluate(lineProblems);
    measured += r.measured;
    for (const p of r.problems) bad("I", c.name, `${story}: ${p}`);
  }
  if (measured === 0) bad("I", c.name, "no lines were measured");
  log.push(`I [${c.name}] ${measured} line ends measured`);
}

/* ---------------------------------------------------------------- H */
async function checkH(page, c) {
  // Wide, and also short: a stage taller than a laptop's window must not clip.
  for (const [w, h] of [[1440, 900], [1440, 640]]) {
    await page.setViewportSize({ width: w, height: h });
    const m = await page.evaluate(async () => {
      const track = document.querySelector(".how-track");
      const header = document.querySelector("header");
      const top = track.getBoundingClientRect().top + window.scrollY;
      // The page smooth-scrolls; an instant jump is what two frames can wait for.
      window.scrollTo({ top: top + 0.4 * (track.offsetHeight - window.innerHeight), behavior: "instant" });
      await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
      const stage = document.querySelector(".how-stage").getBoundingClientRect();
      const inStage = (s) => {
        const e = document.querySelector(s)?.getBoundingClientRect();
        return e ? { top: e.top - stage.top, bottom: stage.bottom - e.bottom } : null;
      };
      return {
        stageTop: stage.top,
        navBottom: header.getBoundingClientRect().bottom,
        on: document.querySelector('.step[data-step="3"]')?.classList.contains("on") ?? null,
        enhanced: document.querySelector(".how")?.classList.contains("is-enhanced"),
        head: inStage(".how-h"),
        steps: inStage(".steps"),
        dg: inStage(".how-dg .dg"),
      };
    });
    const at = `${c.name} ${w}x${h}`;
    if (!m.enhanced) bad("H", at, ".how is not .is-enhanced");
    if (Math.abs(m.stageTop - m.navBottom) > 2) bad("H", at, `.how-stage top ${m.stageTop.toFixed(1)} is not at the nav's bottom ${m.navBottom.toFixed(1)}`);
    if (!m.on) bad("H", at, "step 3 is not .on at 40% of the track");
    for (const k of ["head", "steps", "dg"]) {
      if (!m[k]) bad("H", at, `no ${k} element in the stage`);
      else if (m[k].top < -1 || m[k].bottom < -1) bad("H", at, `${k} is clipped by the stage (${m[k].top.toFixed(0)}px from its top, ${m[k].bottom.toFixed(0)}px from its bottom)`);
    }
  }
  await page.setViewportSize({ width: 1440, height: 900 });
}

/* ---------------------------------------------------------------- J */
async function checkJ(page, c) {
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
    return { h1: read("[data-hero] h1"), lede: read("[data-hero] .hero-lede") };
  });
  log.push(`J [${c.name}] h1 ${m.h1?.toFixed(2)}:1, lede ${m.lede?.toFixed(2)}:1 (min ${MIN_CONTRAST})`);
  if (m.h1 === null) bad("J", c.name, "no [data-hero] h1");
  else if (m.h1 < MIN_CONTRAST) bad("J", c.name, `the h1 is ${m.h1.toFixed(2)}:1 on the page ground, under ${MIN_CONTRAST}:1`);
  if (m.lede === null) bad("J", c.name, "no [data-hero] .hero-lede");
  else if (m.lede < MIN_CONTRAST) bad("J", c.name, `the hero lede is ${m.lede.toFixed(2)}:1 on the page ground, under ${MIN_CONTRAST}:1`);
}

/* ---------------------------------------------------------------- K */
async function checkK(browser, origin, c) {
  const ctx = await browser.newContext({ viewport: { width: c.width, height: c.height }, isMobile: true, hasTouch: true });
  try {
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on("console", (msg) => {
      if (msg.type() === "error") bad("B", `${c.name} K`, `console error: ${msg.text()}`);
    });
    page.on("pageerror", (err) => bad("B", `${c.name} K`, `uncaught: ${err.message}`));
    await page.addInitScript(() => {
      window.__shift = 0;
      window.__lcp = null;
      new PerformanceObserver((l) => {
        for (const e of l.getEntries()) if (!e.hadRecentInput) window.__shift += e.value;
      }).observe({ type: "layout-shift", buffered: true });
      new PerformanceObserver((l) => {
        for (const e of l.getEntries()) {
          window.__lcp = { start: e.startTime, inHero: !!e.element?.closest?.("[data-hero]"), tag: e.element?.tagName ?? null };
        }
      }).observe({ type: "largest-contentful-paint", buffered: true });
    });
    await page.goto(origin + "/", { waitUntil: "load" });
    await page.waitForTimeout(3000);
    const m = await page.evaluate(() => ({ shift: window.__shift, lcp: window.__lcp }));
    log.push(`K [${c.name}] layout shift ${m.shift.toFixed(4)}, LCP <${m.lcp?.tag}> at ${m.lcp?.start.toFixed(0)}ms (local timing, not asserted)`);
    if (m.shift > MAX_SHIFT) bad("K", c.name, `cumulative layout shift ${m.shift.toFixed(3)} is over ${MAX_SHIFT}`);
    if (!m.lcp) bad("K", c.name, "no largest-contentful-paint entry");
    else if (!m.lcp.inHero) bad("K", c.name, `the LCP element (<${m.lcp.tag}>) is not inside [data-hero]`);
  } finally {
    await ctx.close();
  }
}

/* ---------------------------------------------------------------- L */
/** The failover autoplays on a short phone: its trigger is the frame, not the
 * section, which is taller than two of these screens. */
async function checkL(browser, origin) {
  const name = "375x667 js";
  const ctx = await browser.newContext({ viewport: { width: 375, height: 667 }, isMobile: true, hasTouch: true, reducedMotion: "no-preference" });
  try {
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on("console", (msg) => {
      if (msg.type() === "error") bad("B", name, `console error: ${msg.text()}`);
    });
    page.on("pageerror", (err) => bad("B", name, `uncaught: ${err.message}`));
    await page.goto(origin + "/", { waitUntil: "load" });
    await settle(page);
    await page.evaluate(() => {
      const f = document.querySelector("[data-failover] [data-frame]");
      const r = f.getBoundingClientRect();
      window.scrollTo({ top: window.scrollY + r.top + r.height / 2 - window.innerHeight / 2, behavior: "instant" });
    });
    try {
      await page.waitForFunction(() => document.querySelector("[data-failover] [data-frame]")?.dataset.state !== "live", null, { timeout: 5000 });
    } catch {
      bad("L", name, "the failover frame, centred on screen, never left \"live\" within 5s: autoplay did not start");
    }
  } finally {
    await ctx.close();
  }
}

/* ---------------------------------------------------------------- run */
const srv = await serve();
const origin = `http://127.0.0.1:${srv.address().port}`;
let browser;
try {
  browser = await chromium.launch();
  for (const c of CONTEXTS) {
    const ctx = await browser.newContext({
      viewport: { width: c.width, height: c.height },
      javaScriptEnabled: c.js,
      reducedMotion: c.reduced ? "reduce" : "no-preference",
      ...(c.phone ? { isMobile: true, hasTouch: true } : {}),
    });
    const page = await ctx.newPage();
    page.setDefaultTimeout(5000);
    // B: every console error and uncaught exception, in every context.
    page.on("console", (msg) => {
      if (msg.type() === "error") bad("B", c.name, `console error: ${msg.text()}`);
    });
    page.on("pageerror", (err) => bad("B", c.name, `uncaught: ${err.message}`));
    try {
      const res = await page.goto(origin + "/", { waitUntil: "load" });
      if (!res?.ok()) {
        bad("A", c.name, `HTTP ${res?.status()}`);
        continue;
      }
      if (c.js) {
        try {
          await settle(page);
        } catch {
          bad("A", c.name, "the page's script never un-hid the story tabs (no boot)");
        }
      }
      if (c.js && !c.reduced) {
        await check("A", c.name, () => checkA(page, c));
        if (!c.phone) await check("C", c.name, () => checkC(page, c));
        await check("G/I", c.name, () => checkGI(page, c));
        if (!c.phone) {
          await check("J", c.name, () => checkJ(page, c));
          await check("H", c.name, () => checkH(page, c));
        }
        await check("E", c.name, () => checkE(page, c));
      }
      if (c.js && c.reduced) await check("D", c.name, () => checkD(page, c));
      if (!c.js) await check("F", c.name, () => checkF(page, c));
    } finally {
      await ctx.close();
    }
  }
  const phone = CONTEXTS.find((c) => c.phone && c.js && !c.reduced);
  await check("K", phone.name, () => checkK(browser, origin, phone));
  await check("L", "375x667 js", () => checkL(browser, origin));
} finally {
  await browser?.close();
  srv.close();
}

for (const l of log) console.log(l);
if (fail.length) {
  const seen = new Set();
  const uniq = fail.filter((f) => !seen.has(f) && seen.add(f));
  console.error(`home page: ${uniq.length} problem(s)\n  ${uniq.join("\n  ")}`);
  process.exit(1);
}
console.log(`home page passed (${CONTEXTS.length} contexts)`);
