import type { StreamDeckKey } from "./keys.js";
import en from "./locales/en.json" with { type: "json" };
import de from "./locales/de.json" with { type: "json" };
import es from "./locales/es.json" with { type: "json" };
import fr from "./locales/fr.json" with { type: "json" };
import id from "./locales/id.json" with { type: "json" };
import it from "./locales/it.json" with { type: "json" };
import ja from "./locales/ja.json" with { type: "json" };
import ko from "./locales/ko.json" with { type: "json" };
import nl from "./locales/nl.json" with { type: "json" };
import pl from "./locales/pl.json" with { type: "json" };
import ptBR from "./locales/pt-BR.json" with { type: "json" };
import ru from "./locales/ru.json" with { type: "json" };
import tr from "./locales/tr.json" with { type: "json" };
import uk from "./locales/uk.json" with { type: "json" };
import zhHans from "./locales/zh-Hans.json" with { type: "json" };

export type Catalog = Record<StreamDeckKey, string>;

/** The fifteen locales the polyemesis UI ships. Mirrors ui/src/lib/i18n/*.json. */
export const CATALOGS: Readonly<Record<string, Catalog>> = {
  de, en, es, fr, id, it, ja, ko, nl, pl, "pt-BR": ptBR, ru, tr, uk, "zh-Hans": zhHans,
};

/**
 * Picks the catalog for the language Stream Deck reports. Stream Deck names
 * Chinese zh_CN and zh_TW; the UI ships zh-Hans and no Traditional catalog, so
 * zh_TW falls back to English rather than showing Simplified characters.
 */
export function localeFor(sdkLanguage: string | undefined): string {
  if (!sdkLanguage) return "en";
  const normalised = sdkLanguage.replace("_", "-");
  if (normalised === "zh-CN") return "zh-Hans";
  return Object.hasOwn(CATALOGS, normalised) ? normalised : "en";
}

/** Looks a key up in a locale and substitutes {name} placeholders. English is the fallback. */
export function t(locale: string, key: StreamDeckKey, vars: Record<string, string | number> = {}): string {
  const template = CATALOGS[locale]?.[key] ?? en[key];
  return template.replace(/\{(\w+)\}/g, (whole, name: string) =>
    Object.hasOwn(vars, name) ? String(vars[name]) : whole,
  );
}
