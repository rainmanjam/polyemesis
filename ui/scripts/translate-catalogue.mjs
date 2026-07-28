// Fill the i18n catalogues from en.json using a local Ollama translation model.
//
//   node scripts/translate-catalogue.mjs                    # every configured language
//   node scripts/translate-catalogue.mjs es ja              # just these
//   OLLAMA=http://host:11434 MODEL=translategemma:12b node scripts/translate-catalogue.mjs
//
// A tool rather than a one-off, because en.json grows: every string added there
// leaves eleven catalogues behind, and re-running this is the only thing that
// keeps them level. Existing translations are preserved by default, so a rerun
// costs one call per language with new keys and nothing at all for the rest.
// Pass --retranslate to redo a language from scratch.
//
// It is deliberately NOT wired into the build. Machine translation belongs
// behind a human decision, not behind `npm run build`.

import { readFileSync, writeFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const HERE = dirname(fileURLToPath(import.meta.url));
const I18N = join(HERE, "..", "src", "lib", "i18n");

const OLLAMA = process.env.OLLAMA ?? "http://192.168.1.186:11434";
const MODEL = process.env.MODEL ?? "translategemma:27b";

// Right-to-left languages are deliberately absent. The UI sets
// document.documentElement.lang but never dir, and the layout is built from 102
// physically-directional Tailwind utilities (pl-, ml-, left-, text-right,
// border-l...) with no logical equivalents in use. Shipping Arabic or Hebrew
// before those are converted to ps-/pe-/ms-/me-/start-/end- would render a
// mirrored language into an unmirrored layout, which is worse than not offering
// it.
const LANGUAGES = [
  // German is listed so a rerun fills keys added to en.json since it was
  // written. Its existing entries are hand-translated and are preserved: this
  // script never overwrites what is already there unless --retranslate says so.
  { code: "de", label: "Deutsch", name: "German" },
  { code: "es", label: "Español", name: "Spanish" },
  { code: "pt-BR", label: "Português (Brasil)", name: "Brazilian Portuguese" },
  { code: "fr", label: "Français", name: "French" },
  { code: "it", label: "Italiano", name: "Italian" },
  { code: "nl", label: "Nederlands", name: "Dutch" },
  { code: "pl", label: "Polski", name: "Polish" },
  { code: "tr", label: "Türkçe", name: "Turkish" },
  { code: "ru", label: "Русский", name: "Russian" },
  { code: "uk", label: "Українська", name: "Ukrainian" },
  { code: "ja", label: "日本語", name: "Japanese" },
  { code: "ko", label: "한국어", name: "Korean" },
  { code: "zh-Hans", label: "简体中文", name: "Simplified Chinese" },
  { code: "id", label: "Bahasa Indonesia", name: "Indonesian" },
];

// Product and broadcast terms that stay in English, following the precedent the
// hand-written German catalogue already set: these are page names and protocol
// jargon, and an operator reading a forum post about "Playout" needs to find
// the same word in their own UI.
const KEEP_ENGLISH = new Set(["nav.routing", "nav.playout", "chrome.ingest"]);

// Chunked so a long reply cannot run past the model's attention and drop the
// tail. Small enough to always come back complete, large enough that a whole
// language is two or three calls.
const CHUNK = 14;

const args = process.argv.slice(2);
const retranslate = args.includes("--retranslate");
const only = args.filter((a) => !a.startsWith("--"));

const en = JSON.parse(readFileSync(join(I18N, "en.json"), "utf8"));
const KEYS = Object.keys(en);

async function generate(prompt) {
  const res = await fetch(`${OLLAMA}/api/generate`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      model: MODEL,
      prompt,
      stream: false,
      // Deterministic: two runs of this script over the same en.json should
      // produce the same catalogues, or every rerun is an unreviewable diff.
      options: { temperature: 0, num_ctx: 8192 },
    }),
    signal: AbortSignal.timeout(600_000),
  });
  if (!res.ok) throw new Error(`ollama HTTP ${res.status}`);
  return ((await res.json()).response ?? "").trim();
}

function buildPrompt(lang, values) {
  const numbered = values.map((v, i) => `${i + 1}. ${v}`).join("\n");
  return (
    `Translate each numbered English user-interface string into ${lang.name}.\n` +
    `Rules:\n` +
    `- Output exactly ${values.length} lines, each formatted "N. <translation>", ` +
    `with the same numbering and nothing else. No commentary, no quotes.\n` +
    `- Keep every {placeholder} in braces exactly as written, including {count}.\n` +
    `- These are short labels, buttons and status words in live video streaming ` +
    `software. Keep them short enough for a button.\n` +
    `- Preserve the trailing ellipsis on any string that has one.\n\n` +
    numbered
  );
}

/** Parses "N. text" back into an ordered array, tolerating stray blank lines. */
function parseNumbered(out, want) {
  const byIndex = new Map();
  for (const line of out.split("\n")) {
    const m = /^\s*(\d+)\s*[.)]\s*(.*)$/.exec(line);
    if (!m) continue;
    const n = Number(m[1]);
    if (n >= 1 && n <= want && !byIndex.has(n)) byIndex.set(n, m[2].trim());
  }
  return Array.from({ length: want }, (_, i) => byIndex.get(i + 1) ?? null);
}

const placeholders = (s) => (s.match(/\{[a-zA-Z0-9_]+\}/g) ?? []).sort().join(",");

/** Rejects a translation that would be worse than falling back to English. */
function reject(source, translated) {
  if (!translated) return "no output";
  // A dropped {count} renders a sentence with a hole where the number goes.
  if (placeholders(source) !== placeholders(translated)) return "placeholder changed";
  // The model occasionally echoes its own numbering into the text.
  if (/^\s*\d+\s*[.)]/.test(translated)) return "numbering leaked into the text";
  // A label six times its English length has been explained rather than
  // translated, and will overflow whatever it sits in.
  if (translated.length > Math.max(40, source.length * 6)) return "implausibly long";
  return null;
}

async function translateLanguage(lang) {
  const path = join(I18N, `${lang.code}.json`);
  const existing = !retranslate && existsSync(path)
    ? JSON.parse(readFileSync(path, "utf8"))
    : {};

  const todo = KEYS.filter((k) => !KEEP_ENGLISH.has(k) && existing[k] === undefined);
  const out = { ...existing };
  // Forced English terms are written explicitly rather than omitted, so the
  // catalogue shows what it decided rather than silently falling through.
  for (const k of KEYS) if (KEEP_ENGLISH.has(k)) out[k] = en[k];

  if (todo.length === 0) {
    console.log(`  ${lang.code.padEnd(8)} already complete (${KEYS.length} keys)`);
    return writeCatalogue(path, out);
  }

  let skipped = 0;
  for (let i = 0; i < todo.length; i += CHUNK) {
    const keys = todo.slice(i, i + CHUNK);
    const got = parseNumbered(await generate(buildPrompt(lang, keys.map((k) => en[k]))), keys.length);
    keys.forEach((k, j) => {
      const why = reject(en[k], got[j]);
      if (why) {
        // Left absent on purpose. The catalogue type is Partial, and a missing
        // key falls back to English -- which is always better than a broken
        // string, and is exactly what the fallback exists for.
        console.warn(`    ${lang.code} ${k}: ${why}, leaving English`);
        skipped++;
        return;
      }
      out[k] = got[j];
    });
  }
  console.log(
    `  ${lang.code.padEnd(8)} ${todo.length - skipped}/${todo.length} translated` +
      (skipped ? `, ${skipped} left English` : ""),
  );
  writeCatalogue(path, out);
}

/** Writes in en.json key order, so catalogues diff against each other. */
function writeCatalogue(path, cat) {
  const ordered = {};
  for (const k of KEYS) if (cat[k] !== undefined) ordered[k] = cat[k];
  writeFileSync(path, JSON.stringify(ordered, null, 2) + "\n");
}

const targets = only.length
  ? LANGUAGES.filter((l) => only.includes(l.code))
  : LANGUAGES;

if (targets.length === 0) {
  console.error(`no such language. known: ${LANGUAGES.map((l) => l.code).join(" ")}`);
  process.exit(1);
}

console.log(`${MODEL} via ${OLLAMA}\n${KEYS.length} keys, ${targets.length} language(s)\n`);
for (const lang of targets) {
  try {
    await translateLanguage(lang);
  } catch (e) {
    // One language failing must not cost the others their work.
    console.error(`  ${lang.code.padEnd(8)} FAILED: ${e.message}`);
  }
}

// The switcher list, printed rather than written: LANGUAGES in i18n.ts is
// hand-maintained so that adding a catalogue is still a deliberate act.
console.log("\nAdd to LANGUAGES in src/lib/i18n.ts:");
for (const l of targets) console.log(`  { code: "${l.code}", label: "${l.label}" },`);
