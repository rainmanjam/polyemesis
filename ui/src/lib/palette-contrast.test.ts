/// <reference types="node" />
import { beforeEach, describe, expect, it } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

/* WCAG contrast for the palette, computed from the token values themselves.
 *
 * WHY A TEST AND NOT A TABLE IN A DOC. Every ratio this palette was chosen by
 * (the 2026-11 design review) was computed, and a computed number written into
 * a comment is correct on the day it is written and unchecked forever after:
 * the app's --subtle-foreground sat at 3.41:1 on the shell, and its failed pill
 * at 3.88:1, for months, beside comments that called the palette accessible.
 * This file recomputes every pair the palette promises from ui/src/index.css
 * (and the website's @theme) on every run, so retuning a token below a
 * threshold, or reverting one of the 2026-11 changes, fails the commit that
 * does it rather than the next audit.
 *
 * THE MATHS is the review's contrast.mjs: oklch -> oklab -> LMS (cubed) ->
 * linear sRGB (Ottosson's matrices) -> clamp to [0,1] -> relative luminance
 * Y = .2126R + .7152G + .0722B -> (Ylight + .05) / (Ydark + .05). Clamping is
 * what a browser does to an out-of-gamut colour, so the ratio is the one a
 * screen shows. Translucent colours (`bg-primary/85`, `text-foreground/80`)
 * are composited over the ground in gamma-encoded sRGB, which is how a browser
 * blends them. The first block checks the converter against ratios the
 * stylesheets already state, so a broken converter cannot pass everything.
 */

const SRC = fileURLToPath(new URL("../", import.meta.url)); // ui/src/
const ROOT = join(SRC, "..", "..");
const APP_CSS = readFileSync(join(SRC, "index.css"), "utf8");
const SITE_CSS = readFileSync(join(ROOT, "web", "src", "styles", "global.css"), "utf8");
const BUTTON = readFileSync(join(SRC, "components", "ui", "button.tsx"), "utf8");

type Oklch = [number, number, number];
type Rgb = [number, number, number];

function rootBlock(css: string): string {
  const start = css.search(/^:root\s*\{$/m);
  if (start < 0) throw new Error("no :root block in ui/src/index.css");
  return css.slice(start, css.indexOf("\n}\n", start));
}

/** Every `--name: value;` in a block, comments stripped first so a value
 *  quoted in a note is not read as a declaration. Values are kept raw. */
function declarations(block: string): Map<string, string> {
  const out = new Map<string, string>();
  const code = block.replace(/\/\*[\s\S]*?\*\//g, "");
  for (const m of code.matchAll(/(--[a-z0-9-]+)\s*:\s*([^;]+);/g)) {
    if (!out.has(m[1])) out.set(m[1], m[2].trim());
  }
  return out;
}

/** The light theme's block: `:root[data-theme="light"] {` ... `}`. It states ONLY
 *  what differs from the dark :root (colours and shadows); type, motion and
 *  radius are shared and read from :root. */
function lightBlock(css: string): string {
  const start = css.search(/^:root\[data-theme="light"\]\s*\{$/m);
  if (start < 0) throw new Error('no :root[data-theme="light"] block in ui/src/index.css');
  return css.slice(start, css.indexOf("\n}\n", start));
}

const DARK = declarations(rootBlock(APP_CSS));
const LIGHT_OWN = declarations(lightBlock(APP_CSS));
/** What the cascade gives the light theme: the light block over the shared :root. */
const LIGHT = new Map([...DARK, ...LIGHT_OWN]);
const THEMES = { dark: DARK, light: LIGHT } as const;
type ThemeName = keyof typeof THEMES;
const THEME_NAMES: ThemeName[] = ["dark", "light"];

/** The palette under test. Reassigned by each theme's beforeEach, so every
 *  helper below (tok, ratio, ...) measures the theme currently being run. */
let APP: Map<string, string> = DARK;
const SITE = declarations(SITE_CSS.slice(SITE_CSS.indexOf("@theme {")));

/** A token's colour, following var() references through either block. */
function tok(name: string, seen: string[] = []): Oklch {
  const key = `--${name}`;
  const raw = APP.get(key) ?? SITE.get(key);
  if (!raw) throw new Error(`${key} is declared in neither index.css :root nor the site @theme`);
  const lit = raw.match(/^oklch\(\s*([\d.]+)%\s+([\d.]+)\s+([\d.]+)\s*\)$/);
  if (lit) return [Number(lit[1]) / 100, Number(lit[2]), Number(lit[3])];
  const ref = raw.match(/^var\(--([a-z0-9-]+)\)$/);
  if (ref && !seen.includes(ref[1])) return tok(ref[1], [...seen, name]);
  throw new Error(`${key}: ${raw} is neither an oklch() literal nor a var()`);
}

function linear([L, C, H]: Oklch): Rgb {
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

const clamp = (v: number) => Math.min(1, Math.max(0, v));
const encode = (v: number) => (v <= 0.0031308 ? 12.92 * v : 1.055 * v ** (1 / 2.4) - 0.055);
const decode = (v: number) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);

/** Gamma-encoded sRGB of a token, optionally at an alpha over a ground. */
function srgb(name: string, alpha = 1, over?: string): Rgb {
  const top = linear(tok(name)).map((v) => encode(clamp(v))) as Rgb;
  if (alpha >= 1 || !over) return top;
  const under = srgb(over);
  return top.map((v, i) => alpha * v + (1 - alpha) * under[i]) as Rgb;
}

function luminanceOf([r, g, b]: Rgb): number {
  return 0.2126 * decode(r) + 0.7152 * decode(g) + 0.0722 * decode(b);
}

function contrast(a: Rgb, b: Rgb): number {
  const [x, y] = [luminanceOf(a), luminanceOf(b)];
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
}

const ratio = (fg: string, bg: string) => contrast(srgb(fg), srgb(bg));

/** Perceptual distance in oklab. 0.02 is roughly a just-noticeable step; the
 *  old --down and --warn, which nobody ever confused, sit 0.20 apart. */
function deltaE(a: string, b: string): number {
  const lab = ([L, C, H]: Oklch) => [L, C * Math.cos((H * Math.PI) / 180), C * Math.sin((H * Math.PI) / 180)];
  const [p, q] = [lab(tok(a)), lab(tok(b))];
  return Math.hypot(p[0] - q[0], p[1] - q[1], p[2] - q[2]);
}

const TEXT = 4.5; // WCAG 1.4.3, small text: every label in this console is small
const NON_TEXT = 3; // WCAG 1.4.11, component boundaries and state indicators
const SURFACES = ["background", "surface", "card", "card-raised", "popover"];

describe("the contrast converter", () => {
  it("finds the palette at all", () => {
    expect(DARK.size).toBeGreaterThan(40);
    expect(LIGHT.size).toBeGreaterThanOrEqual(DARK.size);
    expect(SITE.size).toBeGreaterThan(10);
  });

  beforeEach(() => {
    APP = DARK;
  });

  it("the light block restates every colour token the dark :root declares, and invents none", () => {
    /* LIGHT is DARK with the light block laid over it, so a colour the light
     * block forgot silently stays dark and every contrast check below would
     * still pass on a half-themed palette. Completeness is its own assertion. */
    const colours = (m: Map<string, string>) => [...m].filter(([, v]) => v.startsWith("oklch(")).map(([k]) => k).sort();
    expect(colours(LIGHT_OWN)).toEqual(colours(DARK));
    expect(LIGHT_OWN.has("--shadow-raised") && LIGHT_OWN.has("--shadow-overlay")).toBe(true);
  });

  it("the light surfaces run lightest to darker where the dark ones run darkest to lighter", () => {
    const L = (name: string) => THEMES.light.get(`--${name}`)!.match(/oklch\(([\d.]+)%/)![1];
    expect(Number(L("card"))).toBeGreaterThan(Number(L("surface")));
    expect(Number(L("surface"))).toBeGreaterThan(Number(L("background")));
    expect(Number(L("foreground"))).toBeLessThan(Number(L("muted-foreground")));
    // On light the page text is the DARKEST ink, as on dark it is the lightest.
    expect(Number(L("foreground"))).toBeLessThan(30);
  });

  it("keeps the tally one lamp in both themes: the same red under the same white label", () => {
    for (const name of ["tally", "tally-foreground"]) {
      expect(THEMES.light.get(`--${name}`), name).toBe(THEMES.dark.get(`--${name}`));
    }
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

describe.each(THEME_NAMES)("[%s] text tokens read at AA on every surface", (theme) => {
  beforeEach(() => {
    APP = THEMES[theme];
  });

  const INKS = [
    "foreground",
    "muted-foreground",
    "subtle-foreground",
    "primary-bright",
    "ok",
    "warn",
    "down",
    "idle",
    "cat-cyan",
  ];
  for (const ink of INKS) {
    for (const bg of SURFACES) {
      it(`--${ink} on --${bg}`, () => {
        expect(ratio(ink, bg), `--${ink} on --${bg}`).toBeGreaterThanOrEqual(TEXT);
      });
    }
  }
});

describe.each(THEME_NAMES)("[%s] ink on fills reads at AA", (theme) => {
  beforeEach(() => {
    APP = THEMES[theme];
  });

  it.each([
    // Status pills: the state's ink on its own dim fill, and the identity chip.
    ["ok", "ok-dim"],
    ["warn", "warn-dim"],
    ["down", "down-dim"],
    ["idle", "idle-dim"],
    ["cat-cyan", "cat-cyan-dim"],
    // The tally lamp's lit label, and the unlit housing's.
    ["tally-foreground", "tally"],
    ["muted-foreground", "tally-dim"],
    // Button fills by their own foreground tokens.
    ["primary-foreground", "primary"],
    ["destructive-foreground", "destructive"],
    ["secondary-foreground", "secondary"],
    ["accent-foreground", "accent"],
    // A selected row or active tab.
    ["primary-bright", "primary-dim"],
    // Inert fills: what the app actually sets on bg-secondary, bg-muted and
    // bg-accent (chips, toggles, hovered menu rows).
    ["foreground", "secondary"],
    ["muted-foreground", "secondary"],
    ["foreground", "muted"],
    ["muted-foreground", "muted"],
    ["foreground", "accent"],
    ["muted-foreground", "accent"],
  ])("--%s on --%s", (fg, bg) => {
    expect(ratio(fg, bg)).toBeGreaterThanOrEqual(TEXT);
  });
});

describe.each(THEME_NAMES)("[%s] every Button variant reads at AA, at rest and on hover", (theme) => {
  beforeEach(() => {
    APP = THEMES[theme];
  });

  /* Read from button.tsx rather than restated, so a variant whose hover is
   * edited is measured as edited. A translucent hover (`hover:bg-primary/85`)
   * is composited over each surface a button can sit on: at 85% the old
   * primary hover measured 3.84:1 on --background under its dark ink. */
  const variants = [...BUTTON.matchAll(/^\s{8}([a-z]+): "([^"]+)",$/gm)].map((m) => ({
    name: m[1],
    classes: m[2].split(/\s+/),
  }));

  it("finds the variants", () => {
    expect(variants.map((v) => v.name)).toEqual(
      expect.arrayContaining(["default", "live", "destructive", "outline", "secondary", "ghost", "link"]),
    );
  });

  const COLOUR = /^[a-z][a-z-]*$/;
  const pick = (classes: string[], prefix: string) =>
    classes
      .map((c) => c.match(new RegExp(`^${prefix}-([a-z-]+?)(?:/(\\d+))?$`)))
      .find((m) => m && COLOUR.test(m[1]) && (APP.has(`--${m[1]}`) || m[1] === "transparent"));

  for (const v of [
    "default",
    "live",
    "destructive",
    "outline",
    "secondary",
    "ghost",
    "link",
  ]) {
    it(`${v}`, () => {
      const classes = variants.find((x) => x.name === v)!.classes;
      // A variant that sets no text colour inherits the body's --foreground.
      const text = pick(classes, "text")?.[1] ?? "foreground";
      const hoverText = pick(classes, "hover:text")?.[1] ?? text;
      const bg = pick(classes, "bg");
      const hoverBg = pick(classes, "hover:bg") ?? bg;
      for (const ground of SURFACES) {
        const fill = (m: RegExpMatchArray | null | undefined) =>
          !m || m[1] === "transparent" ? srgb(ground) : srgb(m[1], m[2] ? Number(m[2]) / 100 : 1, ground);
        const rest = contrast(srgb(text!), fill(bg));
        const hover = contrast(srgb(hoverText!), fill(hoverBg));
        expect(rest, `${v} at rest on --${ground}`).toBeGreaterThanOrEqual(TEXT);
        expect(hover, `${v} on hover on --${ground}`).toBeGreaterThanOrEqual(TEXT);
      }
    });
  }
});

describe.each(THEME_NAMES)("[%s] non-text indicators clear 3:1", (theme) => {
  beforeEach(() => {
    APP = THEMES[theme];
  });

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
    // A text field's edge (WCAG 1.4.11): against every panel a field sits on,
    // and against the field's own --background fill inside it.
    ["input", "card"],
    ["input", "card-raised"],
    ["input", "popover"],
    ["input", "muted"],
    ["input", "surface"],
    ["input", "background"],
    // Meter segments and the peak-hold tick in their trough.
    ["meter-low", "meter-bg"],
    ["meter-mid", "meter-bg"],
    ["meter-peak", "meter-bg"],
    ["meter-hold", "meter-bg"],
    // The website's track chips.
    ["color-meter", "card"],
    ["color-cohost", "card"],
    ["color-game", "card"],
  ].filter(([fg]) => theme === "dark" || !fg.startsWith("color-")))("--%s against --%s", (fg, bg) => {
    expect(ratio(fg, bg)).toBeGreaterThanOrEqual(NON_TEXT);
  });
});

describe.each(THEME_NAMES)("[%s] the 2026-11 decisions hold", (theme) => {
  beforeEach(() => {
    APP = THEMES[theme];
  });

  /* Each of these is a revert guard: the threshold tests above would let
   * several of the old values back in (the old --ring clears 3:1, the old
   * --down-dim still gives the new --down 4.5:1), so the intent is asserted
   * directly. */
  it("draws the focus ring in --primary-bright, at text contrast on a card", () => {
    expect(tok("ring")).toEqual(tok("primary-bright"));
    expect(ratio("ring", "card")).toBeGreaterThanOrEqual(TEXT);
  });

  it("keeps idle neutral: a grey, not a hue", () => {
    expect(tok("idle")[1], "--idle chroma").toBeLessThanOrEqual(0.04);
    expect(tok("idle-dim")[1], "--idle-dim chroma").toBeLessThanOrEqual(0.04);
  });

  it("makes failed, destructive and the meter's red one colour", () => {
    expect(tok("destructive")).toEqual(tok("down"));
    expect(tok("meter-peak")).toEqual(tok("down"));
  });

  it("gives --down-dim the hue of --down, so the failed pill reads as one colour", () => {
    expect(Math.abs(tok("down-dim")[2] - tok("down")[2])).toBeLessThanOrEqual(5);
  });

  it("puts contrasting ink on the destructive fill: dark ink on the light coral, light ink on the deep red", () => {
    const ink = luminanceOf(srgb("destructive-foreground"));
    const fill = luminanceOf(srgb("destructive"));
    if (theme === "dark") expect(ink).toBeLessThan(fill);
    else expect(ink).toBeGreaterThan(fill);
  });

  it.runIf(theme === "dark")("lights the website's crosspoint in the interaction colour", () => {
    expect(SITE.get("--color-cross")).toBe("var(--primary)");
    expect(SITE.get("--color-cross-dim")).toBe("var(--primary-dim)");
  });

  it("no longer has an amber meter stop: amber means warn", () => {
    expect(APP.has("--meter-high")).toBe(false);
  });
});

describe.each(THEME_NAMES)("[%s] the signal colours stay apart", (theme) => {
  beforeEach(() => {
    APP = THEMES[theme];
  });

  /* ON AIR and FAILED were one red: the old --down sat 0.074 from the tally,
   * which at 8px is the same lamp. The floor is half the distance between the
   * old --down and --warn, which nobody ever confused. */
  it.each([
    ["tally", "down"],
    ["tally", "destructive"],
    ["tally", "meter-peak"],
    ["down", "warn"],
    ["tally", "warn"],
    ["meter-mid", "warn"],
    ["color-cohost", "meter-mid"],
    // The identity colour must never be mistaken for a state.
    ["cat-cyan", "ok"],
    ["cat-cyan", "warn"],
    ["cat-cyan", "down"],
    ["cat-cyan", "tally"],
  ].filter(([a]) => theme === "dark" || !a.startsWith("color-")))("--%s and --%s are told apart (ΔEOK ≥ 0.1)", (a, b) => {
    expect(deltaE(a, b)).toBeGreaterThanOrEqual(0.1);
  });
});

describe("the pre-paint colours are the tokens", () => {
  /* Three places hold --surface as a hex because none of them can read a CSS
   * variable: index.html's canvas rule (before the stylesheet), the theme-color
   * meta (the browser's own chrome) and theme-init.js. A palette retune that
   * forgets them leaves a flash of the OLD page colour on every load. */
  const hexOf = (theme: ThemeName) => {
    APP = THEMES[theme];
    const hex = srgb("surface")
      .map((v) => Math.round(v * 255).toString(16).padStart(2, "0"))
      .join("");
    APP = DARK;
    return `#${hex}`;
  };
  const INDEX = readFileSync(join(SRC, "..", "index.html"), "utf8");
  const INIT = readFileSync(join(SRC, "..", "public", "theme-init.js"), "utf8");
  const THEME_TS = readFileSync(join(SRC, "lib", "theme.ts"), "utf8");

  it("authorColor.ts darkens author names against --card-raised of the light theme", () => {
    APP = LIGHT;
    const hex = srgb("card-raised")
      .map((v) => Math.round(v * 255).toString(16).padStart(2, "0"))
      .join("");
    APP = DARK;
    expect(readFileSync(join(SRC, "lib", "authorColor.ts"), "utf8")).toContain(`LIGHT_CHAT_GROUND = "#${hex}"`);
  });

  it.each(THEME_NAMES)("%s: index.html, theme.ts and theme-init.js carry --surface's hex", (theme) => {
    const hex = hexOf(theme);
    const canvas = theme === "dark" ? /html \{ background-color: (#[0-9a-f]{6})/ : /html\[data-theme="light"\] \{ background-color: (#[0-9a-f]{6})/;
    expect(INDEX.match(canvas)?.[1], "index.html canvas rule").toBe(hex);
    expect(THEME_TS, "lib/theme.ts THEME_COLOR").toContain(`${theme}: "${hex}"`);
    expect(INIT, "public/theme-init.js").toContain(`"${hex}"`);
  });
});

describe("text on a fixed-black video well uses the on-video tokens, in both themes", () => {
  /* A preview well, a clip's proxy slot and the badge over a live picture are
   * BLACK in both themes (a video letterbox does not go white). Text drawn on
   * them therefore cannot take a themed token: --muted-foreground is dark in
   * the light theme (1.65:1 on black) and only 3.13:1 in dark. They get the
   * --color-on-video* set, literals that do not follow the theme. */
  const THEME_BLOCK = APP_CSS.slice(APP_CSS.indexOf("@theme inline {"));
  const onVideo = (name: string): Oklch => {
    const m = THEME_BLOCK.match(new RegExp(`--color-${name}:\\s*oklch\\(\\s*([\\d.]+)%\\s+([\\d.]+)\\s+([\\d.]+)\\s*\\)`));
    if (!m) throw new Error(`--color-${name} is not an oklch literal in @theme inline`);
    return [Number(m[1]) / 100, Number(m[2]), Number(m[3])];
  };
  const BLACK: Rgb = [0, 0, 0];
  const rgbOf = (c: Oklch) => linear(c).map((v) => encode(clamp(v))) as Rgb;

  it.each(["on-video", "on-video-dim", "on-video-warn"])("--color-%s reads at AA on #000", (name) => {
    expect(contrast(rgbOf(onVideo(name)), BLACK)).toBeGreaterThanOrEqual(TEXT);
  });

  it("keeps every themed text colour off a bg-black element", () => {
    const hits: string[] = [];
    for (const f of APP_SOURCES) {
      for (const m of readFileSync(f, "utf8").matchAll(/(?:"[^"\n]*|`[^`\n]*)\bbg-black\b[^"`\n]*["`]/g)) {
        if (/\btext-(muted-foreground|subtle-foreground|foreground|warn|ok|down|primary-bright|idle)\b/.test(m[0])) {
          hits.push(`${f.slice(ROOT.length)}: ${m[0]}`);
        }
      }
    }
    // The public player is a separate, always-dark page with its own palette.
    expect(hits.filter((h) => !h.includes("PublicPlayer"))).toEqual([]);
  });

  it("paints the clip editor's no-proxy text and the preview badge with the on-video tokens", () => {
    const clip = readFileSync(join(SRC, "pages", "ClipEditor.tsx"), "utf8");
    expect(clip).toMatch(/text-on-video-dim[^>]*>\s*\{t\("clipedit\.noProxy"\)/);
    const prev = readFileSync(join(SRC, "components", "PreviewPlayer.tsx"), "utf8");
    expect(prev).toContain("text-on-video-warn");
    expect(prev).not.toMatch(/bg-black\/70[^\n]*\n[^\n]*\n[^\n]*text-warn/);
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

const APP_SOURCES = files(SRC, /\.(tsx?|css)$/).filter((f) => !/\.test\.tsx?$/.test(f));
const SOURCES = [...APP_SOURCES, ...files(join(ROOT, "web", "src"), /\.(astro|tsx?|css)$/)];
const PREFIX = String.raw`(?<![\w-])(?:[\w[\]&=>*.-]+:)*`;

function scan(list: string[], re: RegExp): string[] {
  const hits: string[] = [];
  for (const f of list) {
    for (const m of readFileSync(f, "utf8").matchAll(re)) hits.push(`${f.slice(ROOT.length)}: ${m[0]}`);
  }
  return hits;
}

describe("token roles hold in the sources", () => {
  beforeEach(() => {
    APP = DARK;
  });

  it("scans the app and the website", () => {
    expect(APP_SOURCES.length).toBeGreaterThan(100);
    expect(SOURCES.length).toBeGreaterThan(APP_SOURCES.length);
  });

  /* --primary is a fill. As text it measures 4.49:1 on --card, under AA, so
   * every blue that is ink -- a link, an accent label, a hover colour -- is
   * --primary-bright. */
  it("paints no text with --primary", () => {
    const hits = scan(SOURCES, new RegExp(`${PREFIX}(?:text|decoration)-primary(?![\\w-])`, "g"));
    expect(hits, "use text-primary-bright: --primary is fill-only").toEqual([]);
  });

  /* The tally is a solid lamp under its own white label, never ink: tally
   * red as text on a dark surface is the "on air" signal shrunk to a word,
   * and at 3.47:1 on --card it is under AA besides. */
  it("never uses --tally as ink", () => {
    const hits = scan(SOURCES, new RegExp(`${PREFIX}(?:text|decoration)-tally(?![\\w-])`, "g"));
    expect(hits, "tally is a lamp (bg-tally + text-tally-foreground), never text").toEqual([]);
  });

  /* An opacity modifier on a text colour (`text-primary-bright/70`) is a
   * new colour nobody measured: that one composited to 4.14:1 on a card.
   * Each one in the app is composited over every surface and held to AA. */
  it("dims no text token below AA with an opacity modifier", () => {
    const failures: string[] = [];
    let measured = 0;
    for (const f of APP_SOURCES) {
      for (const m of readFileSync(f, "utf8").matchAll(new RegExp(`${PREFIX}text-([a-z][a-z-]*)/(\\d+)(?![\\w-])`, "g"))) {
        const [token, pct] = [m[1], Number(m[2])];
        if (!APP.has(`--${token}`)) {
          failures.push(`${f.slice(ROOT.length)}: text-${token}/${pct} names no colour token`);
          continue;
        }
        for (const theme of THEME_NAMES) {
          if (!THEMES[theme].has(`--${token}`)) continue;
          APP = THEMES[theme];
          measured++;
          for (const ground of SURFACES) {
            const r = contrast(srgb(token, pct / 100, ground), srgb(ground));
            if (r < TEXT)
              failures.push(`${f.slice(ROOT.length)}: text-${token}/${pct} on --${ground} [${theme}] is ${r.toFixed(2)}:1`);
          }
        }
        APP = DARK;
      }
    }
    expect(failures, "use a full-opacity token that passes (text-muted-foreground for secondary)").toEqual([]);
    expect(measured, "the scan should still see the remaining dimmed text").toBeGreaterThan(0);
  });
});
