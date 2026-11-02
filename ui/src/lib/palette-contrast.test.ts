/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

/* WCAG contrast for the palette, computed from the token values themselves.
 *
 * WHY A TEST AND NOT A TABLE IN A DOC. Every ratio this palette was chosen by
 * (the 2026-11 design review) was computed, and a computed number written into
 * a comment is correct on the day it is written and unchecked forever after:
 * the app's --subtle-foreground sat at 3.41:1 on the shell, and its failed pill
 * at 3.88:1, for months, beside comments that called the palette accessible.
 * This file recomputes every pair the palette promises from ui/src/index.css on
 * every run, so retuning a token below a threshold fails the commit that does
 * it rather than the next audit.
 *
 * THE MATHS is the review's contrast.mjs: oklch -> oklab -> LMS (cubed) ->
 * linear sRGB (Ottosson's matrices) -> clamp to [0,1] -> relative luminance
 * Y = .2126R + .7152G + .0722B -> (Ylight + .05) / (Ydark + .05). Clamping is
 * what a browser does to an out-of-gamut colour, so the ratio is the one a
 * screen shows. The first block checks the converter against ratios the
 * stylesheets already state, so a broken converter cannot pass everything.
 */

const SRC = new URL("../", import.meta.url).pathname; // ui/src/
const ROOT = join(SRC, "..", "..");
const APP_CSS = readFileSync(join(SRC, "index.css"), "utf8");
const SITE_CSS = readFileSync(join(ROOT, "web", "src", "styles", "global.css"), "utf8");

type Oklch = [number, number, number];

function rootBlock(css: string): string {
  const start = css.search(/^:root\s*\{$/m);
  if (start < 0) throw new Error("no :root block in ui/src/index.css");
  return css.slice(start, css.indexOf("\n}\n", start));
}

/** Every `--name: oklch(L% C H)` literal in a block. Comments are stripped
 *  first, so a value quoted in a note is not read as a declaration. */
function literals(block: string): Map<string, Oklch> {
  const out = new Map<string, Oklch>();
  const code = block.replace(/\/\*[\s\S]*?\*\//g, "");
  for (const m of code.matchAll(/(--[a-z0-9-]+)\s*:\s*oklch\(\s*([\d.]+)%\s+([\d.]+)\s+([\d.]+)\s*\)\s*;/g)) {
    out.set(m[1], [Number(m[2]) / 100, Number(m[3]), Number(m[4])]);
  }
  return out;
}

const TOKENS = literals(rootBlock(APP_CSS));
const SITE_THEME = literals(SITE_CSS.slice(SITE_CSS.indexOf("@theme {")));

function tok(name: string): Oklch {
  const v = TOKENS.get(`--${name}`) ?? SITE_THEME.get(`--${name}`);
  if (!v) throw new Error(`--${name} is not an oklch() literal in index.css or the site @theme`);
  return v;
}

function linear([L, C, H]: Oklch): [number, number, number] {
  const h = (H * Math.PI) / 180;
  const a = C * Math.cos(h);
  const b = C * Math.sin(h);
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3;
  const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3;
  const s = (L - 0.0894841775 * a - 1.291485548 * b) ** 3;
  return [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ];
}

function luminance(c: Oklch): number {
  const [r, g, b] = linear(c).map((v) => Math.min(1, Math.max(0, v)));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function ratio(fg: string, bg: string): number {
  const x = luminance(tok(fg));
  const y = luminance(tok(bg));
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
}

/** Perceptual distance in oklab. 0.02 is roughly a just-noticeable step; the
 *  review measured the current --down and --warn, which nobody confuses, at 0.20. */
function deltaE(a: string, b: string): number {
  const lab = ([L, C, H]: Oklch) => [L, C * Math.cos((H * Math.PI) / 180), C * Math.sin((H * Math.PI) / 180)];
  const [p, q] = [lab(tok(a)), lab(tok(b))];
  return Math.hypot(p[0] - q[0], p[1] - q[1], p[2] - q[2]);
}

const TEXT = 4.5; // WCAG 1.4.3, small text: every label in this console is small
const NON_TEXT = 3; // WCAG 1.4.11, component boundaries and state indicators

describe("the contrast converter", () => {
  it("finds the palette at all", () => {
    expect(TOKENS.size).toBeGreaterThan(30);
  });

  it.each([
    // Ratios stated in index.css beside --primary-bright, and the shell text.
    ["primary-bright", "card", 7.13],
    ["primary-bright", "background", 7.8],
    ["primary-bright", "primary-dim", 5.23],
    ["foreground", "background", 15.96],
  ])("reproduces the stated ratio of --%s on --%s (%s)", (fg, bg, want) => {
    expect(Math.abs(ratio(fg, bg) - want)).toBeLessThan(0.03);
  });
});

describe("text tokens read at AA on every surface", () => {
  const SURFACES = ["background", "surface", "card", "card-raised", "popover"];
  const INKS = [
    "foreground",
    "muted-foreground",
    "subtle-foreground",
    "primary-bright",
    "ok",
    "warn",
    "down",
    "idle",
  ];
  for (const ink of INKS) {
    for (const bg of SURFACES) {
      it(`--${ink} on --${bg}`, () => {
        expect(ratio(ink, bg), `--${ink} on --${bg}`).toBeGreaterThanOrEqual(TEXT);
      });
    }
  }
});

describe("ink on fills reads at AA", () => {
  it.each([
    // Status pills: the state's ink on its own dim fill.
    ["ok", "ok-dim"],
    ["warn", "warn-dim"],
    ["down", "down-dim"],
    ["idle", "idle-dim"],
    // The tally lamp's lit label, and the unlit housing's.
    ["tally-foreground", "tally"],
    ["muted-foreground", "tally-dim"],
    // Buttons: the primary fill and its hover step, the destructive fill (by
    // its token and by the --background class the Button actually uses), and
    // the live action button, which is dark ink on --ok.
    ["primary-foreground", "primary"],
    ["primary-foreground", "primary-bright"],
    ["destructive-foreground", "destructive"],
    ["background", "destructive"],
    ["background", "ok"],
    // A selected row or active tab.
    ["primary-bright", "primary-dim"],
  ])("--%s on --%s", (fg, bg) => {
    expect(ratio(fg, bg)).toBeGreaterThanOrEqual(TEXT);
  });
});

describe("non-text indicators clear 3:1", () => {
  it.each([
    // The focus ring against every ground a focusable control sits on.
    ["ring", "background"],
    ["ring", "surface"],
    ["ring", "card"],
    ["ring", "card-raised"],
    // A lit tally lamp is a shape, read against the strip it sits in.
    ["tally", "card"],
    ["tally", "background"],
    // The primary fill's edge (a button, a checked box, a lit site crosspoint).
    ["primary", "card"],
    // Meter segments and the peak-hold tick in their trough.
    ["meter-low", "meter-bg"],
    ["meter-mid", "meter-bg"],
    ["meter-peak", "meter-bg"],
    ["meter-hold", "meter-bg"],
    // The website's track chips, which are its own literals.
    ["color-meter", "card"],
    ["color-cohost", "card"],
    ["color-game", "card"],
  ])("--%s against --%s", (fg, bg) => {
    expect(ratio(fg, bg)).toBeGreaterThanOrEqual(NON_TEXT);
  });
});

describe("the signal colours stay apart", () => {
  /* ON AIR and FAILED were one red: the old --down sat 0.074 from a tally at
   * this hue, which at 8px is the same lamp. The floor is half the distance
   * between the old --down and --warn, which nobody ever confused. */
  it.each([
    ["tally", "down"],
    ["down", "warn"],
    ["tally", "warn"],
    ["meter-mid", "warn"],
    ["color-cohost", "meter-mid"],
  ])("--%s and --%s are told apart (ΔEOK ≥ 0.1)", (a, b) => {
    expect(deltaE(a, b)).toBeGreaterThanOrEqual(0.1);
  });

  it("no longer has an amber meter stop: amber means warn", () => {
    expect(TOKENS.has("--meter-high")).toBe(false);
  });
});

/* ---------------------------------------- how the tokens may be spent */

function files(dir: string, ext: RegExp): string[] {
  return readdirSync(dir).flatMap((e: string) => {
    const p = join(dir, e);
    if (e === "node_modules") return [];
    return statSync(p).isDirectory() ? files(p, ext) : ext.test(p) ? [p] : [];
  });
}

const SOURCES = [
  ...files(SRC, /\.(tsx?|css)$/).filter((f) => !f.endsWith(".test.ts") && !f.endsWith(".test.tsx")),
  ...files(join(ROOT, "web", "src"), /\.(astro|tsx?|css)$/),
];

describe("token roles hold in the sources", () => {
  it("scans the app and the website", () => {
    expect(SOURCES.length).toBeGreaterThan(100);
  });

  /* --primary is a fill. As text it measures 4.49:1 on --card, under AA, so
   * every blue that is ink -- a link, an accent label, a hover colour -- is
   * --primary-bright. */
  it("paints no text with --primary", () => {
    const hits: string[] = [];
    for (const f of SOURCES) {
      for (const m of readFileSync(f, "utf8").matchAll(/(?<![\w-])(?:[\w[\]&=>*.-]+:)*(?:text|decoration)-primary(?![\w-])/g)) {
        hits.push(`${f.slice(ROOT.length)}: ${m[0]}`);
      }
    }
    expect(hits, "use text-primary-bright: --primary is fill-only").toEqual([]);
  });

  /* The tally is a solid lamp under its own white label, never ink: tally
   * red as text on a dark surface is the "on air" signal shrunk to a word,
   * and at 3.47:1 on --card it is under AA besides. */
  it("never uses --tally as ink", () => {
    const hits: string[] = [];
    for (const f of SOURCES) {
      for (const m of readFileSync(f, "utf8").matchAll(/(?<![\w-])(?:[\w[\]&=>*.-]+:)*(?:text|decoration)-tally(?![\w-])/g)) {
        hits.push(`${f.slice(ROOT.length)}: ${m[0]}`);
      }
    }
    expect(hits, "tally is a lamp (bg-tally + text-tally-foreground), never text").toEqual([]);
  });
});
