import { useCallback, useSyncExternalStore } from "react";

import de from "./i18n/de.json";
import en from "./i18n/en.json";
import es from "./i18n/es.json";
import ptBR from "./i18n/pt-BR.json";
import fr from "./i18n/fr.json";
import it from "./i18n/it.json";
import nl from "./i18n/nl.json";
import pl from "./i18n/pl.json";
import tr from "./i18n/tr.json";
import ru from "./i18n/ru.json";
import uk from "./i18n/uk.json";
import ja from "./i18n/ja.json";
import ko from "./i18n/ko.json";
import zhHans from "./i18n/zh-Hans.json";
import id from "./i18n/id.json";
import type { ProcessState } from "./types";

/* polyemesis is a single-admin console, so this is deliberately a hundred lines
 * rather than an i18n framework: a flat key -> string catalogue, a store the
 * whole app can read without a provider, and English as the fallback. Anything
 * larger would cost more bundle than the feature is worth.
 *
 * Only the shared chrome is extracted so far. Page strings are a mechanical
 * follow-up: add the key here first, then swap the literal at the call site. */

/** English is the source of truth: every key the app may ask for is defined
 *  here, which is what lets a lagging translation degrade to English instead of
 *  rendering a raw key at the user. */
export type TranslationKey = keyof typeof en;

/** A translation may be incomplete, never inventive. */
type Catalogue = Partial<Record<TranslationKey, string>>;

/** Rejects keys English does not define. Without it a key renamed in en.json
 *  leaves a dead entry behind in every other catalogue, and the only symptom is
 *  a string that silently stops translating. */
type NoStrayKeys<T> = T & Record<Exclude<keyof T, TranslationKey>, never>;

function catalogue<T extends Catalogue>(c: NoStrayKeys<T>): Catalogue {
  return c;
}

/** The languages offered in the switcher, in menu order. */
export const LANGUAGES = [
  { code: "en", label: "English" },
  { code: "de", label: "Deutsch" },
  { code: "es", label: "Español" },
  { code: "pt-BR", label: "Português (Brasil)" },
  { code: "fr", label: "Français" },
  { code: "it", label: "Italiano" },
  { code: "nl", label: "Nederlands" },
  { code: "pl", label: "Polski" },
  { code: "tr", label: "Türkçe" },
  { code: "ru", label: "Русский" },
  { code: "uk", label: "Українська" },
  { code: "ja", label: "日本語" },
  { code: "ko", label: "한국어" },
  { code: "zh-Hans", label: "简体中文" },
  { code: "id", label: "Bahasa Indonesia" },
] as const;

export type LanguageCode = (typeof LANGUAGES)[number]["code"];

const CATALOGUES: Record<LanguageCode, Catalogue> = {
  en: catalogue(en),
  de: catalogue(de),
  es: catalogue(es),
  "pt-BR": catalogue(ptBR),
  fr: catalogue(fr),
  it: catalogue(it),
  nl: catalogue(nl),
  pl: catalogue(pl),
  tr: catalogue(tr),
  ru: catalogue(ru),
  uk: catalogue(uk),
  ja: catalogue(ja),
  ko: catalogue(ko),
  "zh-Hans": catalogue(zhHans),
  id: catalogue(id),
};

const STORAGE_KEY = "polyemesis.language";

function isSupported(code: string | null | undefined): code is LanguageCode {
  return LANGUAGES.some((l) => l.code === code);
}

function initialLanguage(): LanguageCode {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (isSupported(stored)) return stored;
  } catch {
    // Safari in private mode throws on any localStorage access. English is a
    // working default, so storage being unavailable must not break the app.
  }
  // Consulted only when the operator has not chosen: honouring the browser on
  // first load is free, and anything we do not ship lands on English rather
  // than on a half-translated catalogue.
  if (typeof navigator === "undefined") return "en";
  for (const tag of navigator.languages ?? [navigator.language]) {
    const match = matchLanguage(tag);
    if (match) return match;
  }
  return "en";
}

/** Resolves a BCP-47 tag from the browser onto a catalogue we ship.
 *
 *  This used to be `navigator.language.slice(0, 2)`, which was right while every
 *  code was two letters and silently wrong the moment regional ones arrived: a
 *  browser reporting `pt-BR` became `pt`, matched nothing, and a Brazilian
 *  operator got English while a Brazilian catalogue sat unused beside it.
 *
 *  Exact match first, then the base language, so `zh-CN` and `zh-SG` both find
 *  `zh-Hans` while `pt-BR` still beats a bare `pt` if we ever ship both. */
function matchLanguage(tag: string): LanguageCode | null {
  const lower = tag.toLowerCase();
  const exact = LANGUAGES.find((l) => l.code.toLowerCase() === lower);
  if (exact) return exact.code;
  const base = lower.split("-")[0];
  const byBase = LANGUAGES.find(
    (l) => l.code.toLowerCase().split("-")[0] === base,
  );
  return byBase ? byBase.code : null;
}

let current: LanguageCode = initialLanguage();
const listeners = new Set<() => void>();

function syncDocumentLanguage(): void {
  // Screen readers take pronunciation from <html lang>. Switching the catalogue
  // without switching this has German read aloud with an English voice.
  if (typeof document !== "undefined") document.documentElement.lang = current;
}
syncDocumentLanguage();

function subscribe(onChange: () => void): () => void {
  listeners.add(onChange);
  return () => {
    listeners.delete(onChange);
  };
}

export function getLanguage(): LanguageCode {
  return current;
}

export function setLanguage(code: LanguageCode): void {
  if (code === current) return;
  current = code;
  try {
    localStorage.setItem(STORAGE_KEY, code);
  } catch {
    // A switch that does not survive a reload still beats one that throws.
  }
  syncDocumentLanguage();
  for (const notify of listeners) notify();
}

export type TranslateParams = Record<string, string | number>;

/** Substitution is `{name}` and nothing else — no plural rules, no dates. When
 *  a string needs more than this, give each form its own key. */
export function translate(
  lang: LanguageCode,
  key: TranslationKey,
  params?: TranslateParams,
): string {
  const template = CATALOGUES[lang][key] ?? en[key];
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (whole, name: string) =>
    name in params ? String(params[name]) : whole,
  );
}

export type Translator = (
  key: TranslationKey,
  params?: TranslateParams,
) => string;

/** Subscribes the component to language changes. */
export function useLanguage(): LanguageCode {
  return useSyncExternalStore(subscribe, getLanguage, getLanguage);
}

/** The hook every component uses: `const t = useT()`, then `t("nav.settings")`.
 *  Re-renders on a language switch because it reads the store. */
export function useT(): Translator {
  const lang = useLanguage();
  return useCallback<Translator>(
    (key, params) => translate(lang, key, params),
    [lang],
  );
}

/** The catalogue key for a process state. Lives here rather than in lib/signal.ts
 *  so the state vocabulary and its translations cannot drift apart; signal.ts's
 *  labelForState stays as the English answer for non-React callers. */
export function stateLabelKey(state?: ProcessState | null): TranslationKey {
  switch (state) {
    case "running":
      return "state.live";
    case "reconnecting":
      return "state.reconnecting";
    case "starting":
      return "state.starting";
    case "failed":
      return "state.failed";
    case "stopped":
      return "state.stopped";
    default:
      return "state.offline";
  }
}

export type StateLabeller = (state?: ProcessState | null) => string;

/** Translated replacement for labelForState, for components that render a
 *  status pill. */
export function useStateLabel(): StateLabeller {
  const t = useT();
  return useCallback<StateLabeller>((state) => t(stateLabelKey(state)), [t]);
}
