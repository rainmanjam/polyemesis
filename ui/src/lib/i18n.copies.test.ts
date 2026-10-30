import { describe, expect, it } from "vitest";

import en from "./i18n/en.json";
import de from "./i18n/de.json";
import es from "./i18n/es.json";
import fr from "./i18n/fr.json";
import id from "./i18n/id.json";
import it_ from "./i18n/it.json";
import ja from "./i18n/ja.json";
import ko from "./i18n/ko.json";
import nl from "./i18n/nl.json";
import pl from "./i18n/pl.json";
import ptBR from "./i18n/pt-BR.json";
import ru from "./i18n/ru.json";
import tr from "./i18n/tr.json";
import uk from "./i18n/uk.json";
import zhHans from "./i18n/zh-Hans.json";

/* TRANSLATED, NOT COPIED (A19).
 *
 * The catalogue tests already catch leftover English inside the non-Latin
 * locales. A Latin-script locale can hide a whole untranslated sentence,
 * because English letters are its letters too: "Delete the rule" in the German
 * file reads as a German file with one line nobody got to. A value of three or
 * more words identical to English is that, except for the few strings that are
 * the same in every language: format templates, product and standard names,
 * and literal example values. Those are listed by key, with the reason. */

const SAME_EVERYWHERE = new Set<string>([
  "caps.cellTitle", // "{column} — {support}. {reason}": a template of slots
  "dest.transportRtmp", // "RTMP / RTMPS": protocol names
  "route.youtubeTwitchSpotify", // product names
  "tour.progress", // "{{current}} / {{total}}"
  "pairing.result", // "{name} · {scope} · prefix {prefix}…": a code-like line
  "rend.marginX", // "Margin X (%)": kept where the locale uses the loanword
  "rend.marginY",
  "route.lufsBroadcast", // "EBU R128 broadcast": a standard's name
  "route.normalizeLoudnorm", // "Loudness (EBU R128)"
  "dash.tagsPlaceholder", // example tags typed as the platform will show them
]);

const LOCALES: Record<string, Record<string, string>> = {
  de, es, fr, id, it: it_, ja, ko, nl, pl, "pt-BR": ptBR, ru, tr, uk, "zh-Hans": zhHans,
};

describe("every locale translates rather than copies", () => {
  it.each(Object.keys(LOCALES))("%s copies no English sentence verbatim", (lang) => {
    const english = en as Record<string, string>;
    const copied = Object.entries(LOCALES[lang])
      .filter(([k, v]) => !SAME_EVERYWHERE.has(k) && v === english[k] && english[k].trim().split(/\s+/).length >= 3)
      .map(([k]) => k);
    expect(copied, `${lang} has English copies instead of translations`).toEqual([]);
  });
});
