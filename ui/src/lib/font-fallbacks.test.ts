import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";

/* Per-script font fallbacks (B3).
 *
 * IBM Plex Sans carries Latin, Greek and Cyrillic. It does not carry Han,
 * Hangul or kana, so a Japanese label fell through to whatever the operating
 * system chose, with no stated intent. The fix is a :lang() rule per script:
 * the same font-sans token, with the platform's CJK faces named in order after
 * Plex, so the browser's own fallback never has to guess.
 *
 * Only faces the operating system already ships are named. Self-hosting a
 * subset of Noto CJK would add megabytes to the binary for a glyph set most
 * installs already have, and it is a decision to make with real screenshots.
 *
 * The guard is a classification, not a rendering: every catalogue must be
 * either in the Latin/Cyrillic list or have a fallback rule. A locale added
 * later without a decision fails here instead of shipping tofu. */

const UI = new URL("../../", import.meta.url).pathname; // ui/
const CSS = readFileSync(join(UI, "src", "index.css"), "utf8");
const LOCALE_DIR = join(UI, "src", "lib", "i18n");

/** Locales whose script Plex covers, so the default stack is correct. */
const PLEX_SCRIPTS = ["en", "de", "es", "fr", "id", "it", "nl", "pl", "pt-BR", "ru", "tr", "uk"];

/** Locales that need a per-script fallback, keyed by the :lang() selector. */
const SCRIPT_FALLBACKS: Record<string, string[]> = {
  ja: ["Hiragino Sans", "Yu Gothic", "Noto Sans CJK JP"],
  ko: ["Apple SD Gothic Neo", "Malgun Gothic", "Noto Sans CJK KR"],
  "zh-Hans": ["PingFang SC", "Microsoft YaHei", "Noto Sans CJK SC"],
};

function ruleFor(lang: string): string | null {
  const m = CSS.match(new RegExp(`:root:lang\\(${lang}\\)\\s*\\{([^}]*)\\}`));
  return m ? m[1] : null;
}

describe("per-script font fallbacks", () => {
  it("every shipped locale is either covered by Plex or has a fallback rule", () => {
    const shipped = readdirSync(LOCALE_DIR)
      .filter((f) => f.endsWith(".json"))
      .map((f) => f.replace(/\.json$/, ""));
    expect(shipped.length).toBe(15);
    const classified = new Set([...PLEX_SCRIPTS, ...Object.keys(SCRIPT_FALLBACKS)]);
    expect(shipped.filter((l) => !classified.has(l))).toEqual([]);
  });

  it.each(Object.entries(SCRIPT_FALLBACKS))("%s names its CJK faces after Plex", (lang, faces) => {
    const rule = ruleFor(lang);
    expect(rule, `:root:lang(${lang}) rule is missing from index.css`).not.toBeNull();
    const decl = rule!.match(/--font-sans:\s*([^;]+);/);
    expect(decl, `${lang} rule sets no --font-sans`).not.toBeNull();
    const stack = decl![1].split(",").map((s) => s.trim().replace(/"/g, ""));
    expect(stack[0]).toBe("IBM Plex Sans");
    for (const face of faces) expect(stack).toContain(face);
    expect(stack.at(-1)).toBe("sans-serif");
  });
});

/* EXTENDED LATIN AND CYRILLIC (B3). The base woff2 files are basic Latin only,
 * so every letter of a translation outside U+0000-00FF must fall inside the
 * unicode-range of an app-only -ext face, or it renders in a system font. */
// Punctuation and symbols a system face may draw without it reading as a
// different typeface. U+02BC, the Ukrainian apostrophe, is here because Plex
// has no glyph for it at any width; the -ext range names it anyway, so a
// future Plex that adds it is picked up.
const EXTRA_OK = /[\u2000-\u206F\u2190-\u21FF\u2212\u2318\u2026\u00A0\u02BC]/;

function rangesOf(file: string): [number, number][] {
  const m = CSS.match(new RegExp(`@font-face\\s*\\{[^}]*url\\("/fonts/${file}"\\)[^}]*unicode-range:\\s*([^;]+);`));
  if (!m) return [];
  return m[1].split(",").map((r) => {
    const [a, b] = r.trim().replace(/^U\+/i, "").split("-");
    return [parseInt(a, 16), parseInt(b ?? a, 16)] as [number, number];
  });
}

describe("extended Latin and Cyrillic faces", () => {
  it.each(["ibm-plex-sans-ext.woff2", "jetbrains-mono-ext.woff2"])("%s is loaded with a unicode-range", (file) => {
    expect(rangesOf(file).length).toBeGreaterThan(0);
  });

  it.each(PLEX_SCRIPTS)("every %s letter is in the base subset or an -ext range", (lang) => {
    const ranges = rangesOf("ibm-plex-sans-ext.woff2");
    const text = Object.values(JSON.parse(readFileSync(join(LOCALE_DIR, `${lang}.json`), "utf8")) as Record<string, string>).join("");
    const uncovered = [...new Set(text)].filter((ch) => {
      const cp = ch.codePointAt(0)!;
      if (cp <= 0xff || EXTRA_OK.test(ch)) return false;
      return !ranges.some(([a, b]) => cp >= a && cp <= b);
    });
    expect(uncovered).toEqual([]);
  });
});

describe("CJK mono fallbacks", () => {
  it.each(Object.entries(SCRIPT_FALLBACKS))("%s names a CJK face in --font-mono too", (lang, faces) => {
    const decl = ruleFor(lang)?.match(/--font-mono:\s*([^;]+);/);
    expect(decl, `${lang} rule sets no --font-mono`).toBeTruthy();
    const stack = decl![1].split(",").map((s) => s.trim().replace(/"/g, ""));
    expect(stack[0]).toBe("JetBrains Mono");
    expect(stack.some((f) => faces.includes(f))).toBe(true);
    expect(stack.at(-1)).toBe("monospace");
  });
});

