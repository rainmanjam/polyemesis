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
