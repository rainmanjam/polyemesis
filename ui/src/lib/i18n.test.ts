import { describe, expect, it } from "vitest";

import en from "./i18n/en.json";
import de from "./i18n/de.json";
import es from "./i18n/es.json";
import fr from "./i18n/fr.json";
import id from "./i18n/id.json";
// `it` is vitest's test function, so Italian is aliased.
import itIT from "./i18n/it.json";
import ja from "./i18n/ja.json";
import ko from "./i18n/ko.json";
import nl from "./i18n/nl.json";
import pl from "./i18n/pl.json";
import ptBR from "./i18n/pt-BR.json";
import ru from "./i18n/ru.json";
import tr from "./i18n/tr.json";
import uk from "./i18n/uk.json";
import zhHans from "./i18n/zh-Hans.json";

/* Mechanical guards on the catalogues.
 *
 * These check the properties a human reviewer cannot check by reading, and
 * deliberately do NOT check whether a translation is good — no test can. What
 * they catch is the class of mistake that turns a translated string into a
 * broken one:
 *
 *   - A translated PLACEHOLDER. `{count}` rendered as `{anzahl}` stops
 *     substituting and the operator sees a literal brace on screen. This is the
 *     single most common machine-translation failure and it is invisible until
 *     someone runs that locale.
 *   - A key English does not define. The type system already rejects this via
 *     NoStrayKeys, but only for a catalogue that is imported and type-checked;
 *     this asserts it against the JSON itself.
 *   - An empty string, which renders as nothing at all rather than falling back
 *     to English — worse than an untranslated string, because it is invisible.
 *
 * Incompleteness is NOT an error. lib/i18n.ts falls back to English per key, so
 * a locale that is behind degrades to readable English; the coverage report at
 * the end of this file makes it visible without failing the build. */

const CATALOGUES: Record<string, Record<string, string>> = {
  de, es, fr, id, it: itIT, ja, ko, nl, pl, "pt-BR": ptBR, ru, tr, uk, "zh-Hans": zhHans,
};

const english = en as Record<string, string>;

/** The `{name}` tokens in a string, which lib/i18n.ts substitutes at render. */
function placeholders(s: string): string[] {
  return (s.match(/\{(\w+)\}/g) ?? []).sort();
}

describe("translation catalogues", () => {
  it.each(Object.keys(CATALOGUES))("%s defines no key English lacks", (lang) => {
    const stray = Object.keys(CATALOGUES[lang]).filter((k) => !(k in english));
    // A key renamed in en.json leaves a dead entry behind here, and the only
    // symptom is a string that silently stops translating.
    expect(stray, `${lang} has keys absent from en.json`).toEqual([]);
  });

  it.each(Object.keys(CATALOGUES))("%s preserves every placeholder", (lang) => {
    const broken: string[] = [];
    for (const [key, value] of Object.entries(CATALOGUES[lang])) {
      const want = placeholders(english[key] ?? "");
      const got = placeholders(value);
      if (want.join() !== got.join()) {
        broken.push(`${key}: expected ${want.join(",") || "none"}, got ${got.join(",") || "none"}`);
      }
    }
    expect(broken, `${lang} corrupted placeholders`).toEqual([]);
  });

  it.each(Object.keys(CATALOGUES))("%s has no empty strings", (lang) => {
    const empty = Object.entries(CATALOGUES[lang])
      .filter(([, v]) => v.trim() === "")
      .map(([k]) => k);
    // An empty value beats the English fallback and renders nothing, so a label
    // simply vanishes.
    expect(empty, `${lang} has empty values that would render as blank`).toEqual([]);
  });

  /* Catches a source string half-translated in place.
   *
   * Editing a long paragraph in a non-Latin script, it is genuinely easy to
   * leave an English connective behind — "バリエーションalso一緒に" reads as a
   * typo to anyone who does not know the source, and no reviewer of the target
   * language would recognise it as a leftover. Restricted to the scripts where
   * a stray Latin word is unambiguous; a Latin-script locale legitimately
   * contains these letters. Technical terms stay in English by design, so the
   * check looks only for English FUNCTION words, which never survive a real
   * translation. */
  const NON_LATIN = ["ru", "uk", "ja", "ko", "zh-Hans"];
  const FUNCTION_WORDS = new Set([
    "also", "and", "the", "with", "that", "this", "from", "when", "which",
    "then", "but", "for", "not", "are", "its", "you", "your", "have", "will",
  ]);

  it.each(NON_LATIN)("%s contains no leftover English function words", (lang) => {
    const leaked: string[] = [];
    for (const [key, value] of Object.entries(CATALOGUES[lang])) {
      for (const word of value.match(/[A-Za-z]{3,}/g) ?? []) {
        if (FUNCTION_WORDS.has(word.toLowerCase())) leaked.push(`${key}: ${word}`);
      }
    }
    expect(leaked, `${lang} has untranslated fragments`).toEqual([]);
  });

  /* Catches text from the WRONG target language.
   *
   * Writing fifteen locales in sequence, a character can carry over from the
   * one before — a Hangul syllable landing in the Japanese catalogue reads as
   * a rare kanji to anyone who does not know Korean, and the Latin-word check
   * above cannot see it because nothing Latin is involved. Each script is only
   * asserted absent where it could never legitimately appear. */
  const FOREIGN_SCRIPTS: Record<string, [string, RegExp][]> = {
    ja: [["Hangul", /[가-힯]/], ["Cyrillic", /[Ѐ-ӿ]/]],
    ko: [["Kana", /[぀-ヿ]/], ["Cyrillic", /[Ѐ-ӿ]/]],
    "zh-Hans": [["Hangul", /[가-힯]/], ["Kana", /[぀-ヿ]/]],
    ru: [["Hangul", /[가-힯]/], ["CJK", /[一-鿿]/]],
    uk: [["Hangul", /[가-힯]/], ["CJK", /[一-鿿]/]],
  };

  it.each(Object.keys(FOREIGN_SCRIPTS))("%s contains no foreign script", (lang) => {
    const found: string[] = [];
    for (const [key, value] of Object.entries(CATALOGUES[lang])) {
      for (const [script, pattern] of FOREIGN_SCRIPTS[lang]) {
        const hit = pattern.exec(value);
        if (hit) found.push(`${key}: ${script} ${JSON.stringify(hit[0])}`);
      }
    }
    expect(found, `${lang} contains characters from another language`).toEqual([]);
  });

  /* Catches an English word spliced into the middle of a non-Latin word.
   *
   * The real defect this is named for: 応answerしない, where "answer" landed
   * inside 応答しない. The function-word check above cannot see it — "answer"
   * is a content word — and no amount of proofreading the English side would
   * reveal it either.
   *
   * KOREAN IS EXCLUDED, and that is not an oversight. Korean particles attach
   * directly to Latin words as a matter of correct orthography — URL이,
   * polyemesis가, FLAC은 are all right — so this check produces nothing but
   * false positives there. The other four scripts separate Latin runs with
   * spaces or punctuation, so a glued one is genuinely a typo. */
  const GLUE_CHECKED = ["ja", "zh-Hans", "ru", "uk"];
  const GLUED = /(?:[぀-ヿ一-鿿Ѐ-ӿ][A-Za-z]{2,})|(?:[A-Za-z]{2,}[぀-ヿ一-鿿Ѐ-ӿ])/;

  it.each(GLUE_CHECKED)("%s has no Latin word glued into a native word", (lang) => {
    const glued: string[] = [];
    for (const [key, value] of Object.entries(CATALOGUES[lang])) {
      const hit = GLUED.exec(value);
      if (hit) glued.push(`${key}: ${JSON.stringify(hit[0])}`);
    }
    expect(glued, `${lang} has a spliced Latin word`).toEqual([]);
  });

  it("English defines no empty strings either", () => {
    const empty = Object.entries(english).filter(([, v]) => v.trim() === "").map(([k]) => k);
    expect(empty).toEqual([]);
  });

  // Not an assertion about completeness — just makes the gap visible, so a
  // locale quietly falling behind is noticed rather than discovered by a user.
  it("reports coverage per locale", () => {
    const total = Object.keys(english).length;
    const rows = Object.entries(CATALOGUES)
      .map(([lang, cat]) => {
        const have = Object.keys(cat).filter((k) => k in english).length;
        return { lang, have, pct: Math.round((have / total) * 100) };
      })
      .sort((a, b) => a.have - b.have);
    // eslint-disable-next-line no-console
    console.log(
      `\n  i18n coverage (${total} keys):\n` +
        rows.map((r) => `    ${r.lang.padEnd(8)} ${String(r.have).padStart(4)}  ${r.pct}%`).join("\n"),
    );
    expect(total).toBeGreaterThan(0);
  });
});
