import { describe, expect, it } from "vitest";
import { CATALOGS, localeFor, t } from "../src/i18n.js";
import { STREAM_DECK_KEYS } from "../src/keys.js";

const placeholders = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();

describe("plugin catalogs", () => {
  it("ships the fifteen locales the UI ships", () => {
    expect(Object.keys(CATALOGS).sort()).toEqual(
      ["de", "en", "es", "fr", "id", "it", "ja", "ko", "nl", "pl", "pt-BR", "ru", "tr", "uk", "zh-Hans"].sort(),
    );
  });

  it.each(Object.keys(CATALOGS))("%s defines every key with a non-empty value", (locale) => {
    const catalog = CATALOGS[locale]!;
    expect(Object.keys(catalog).sort()).toEqual([...STREAM_DECK_KEYS].sort());
    for (const key of STREAM_DECK_KEYS) {
      expect(catalog[key].trim(), `${locale} ${key}`).not.toBe("");
    }
  });

  it.each(Object.keys(CATALOGS))("%s keeps the same placeholders as English", (locale) => {
    const en = CATALOGS.en!;
    for (const key of STREAM_DECK_KEYS) {
      expect(placeholders(CATALOGS[locale]![key]), `${locale} ${key}`).toEqual(placeholders(en[key]));
    }
  });
});

describe("localeFor", () => {
  it("maps the Stream Deck language names onto the catalogs", () => {
    expect(localeFor("de")).toBe("de");
    expect(localeFor("zh_CN")).toBe("zh-Hans");
  });

  it("falls back to English for a language with no catalog, including zh_TW", () => {
    expect(localeFor("zh_TW")).toBe("en");
    expect(localeFor("sv")).toBe("en");
    expect(localeFor(undefined)).toBe("en");
  });
});

describe("t", () => {
  it("substitutes placeholders", () => {
    expect(t("en", "streamdeck.stop.youtube", { count: 2 })).toBe("2 YouTube will end");
    expect(t("ja", "streamdeck.stop.youtube", { count: 3 })).toBe("YouTube 3件が終了します");
  });

  it("leaves an unknown placeholder visible rather than blank", () => {
    expect(t("en", "streamdeck.stop.youtube")).toBe("{count} YouTube will end");
  });
});
