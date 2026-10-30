/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import en from "./i18n/en.json";
import { SETTINGS_INDEX, searchSettingsFields } from "./settingsSearch";

/* THE SEARCH INDEX FOLLOWS THE PAGE.
 *
 * Radix mounts only the open settings tab, so the search cannot read labels
 * off the DOM and reads them from SETTINGS_INDEX instead. A static list rots
 * the day somebody adds a field and forgets it. This holds the two together:
 * every <Label> and <CardTitle> keyed in SettingsPage.tsx is in the index, and
 * every index entry still names something on the page. */

const src = (rel: string) => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), "utf8");

function keysIn(body: string, tags: string[]): Set<string> {
  const out = new Set<string>();
  const re = new RegExp(`<(${tags.join("|")})\\b[^>]*>([\\s\\S]{0,300}?)</\\1>`, "g");
  for (const m of body.matchAll(re)) {
    // The element's own text, not a badge inside it: the key must open it.
    const key = /^\s*\{t\("([^"]+)"/.exec(m[2])?.[1];
    if (key) out.add(key);
  }
  return out;
}

const page = src("../pages/SettingsPage.tsx");
// Cards that live in their own components but render on the settings page.
const elsewhere = [
  ...keysIn(src("../components/AutomodConfig.tsx"), ["CardTitle"]),
  ...keysIn(src("../components/DebugSettings.tsx"), ["CardTitle"]),
];

describe("settings search index", () => {
  it("lists every keyed label and card title on the settings page", () => {
    const indexed = new Set(SETTINGS_INDEX.map((e) => e.key as string));
    const missing = [...keysIn(page, ["Label", "CardTitle"])].filter((k) => !indexed.has(k));
    expect(missing, "add these to SETTINGS_INDEX in lib/settingsSearch.ts").toEqual([]);
  });

  it("names nothing that is no longer on the page", () => {
    const onPage = new Set([...keysIn(page, ["Label", "CardTitle"]), ...elsewhere]);
    const stale = SETTINGS_INDEX.map((e) => e.key as string).filter((k) => !onPage.has(k));
    expect(stale).toEqual([]);
  });

  it("points every anchor at a control id the page renders", () => {
    const broken = SETTINGS_INDEX.filter((e) => e.anchor && !page.includes(`htmlFor="${e.anchor}"`)).map(
      (e) => `${e.key} -> ${e.anchor}`,
    );
    expect(broken).toEqual([]);
  });

  it("searches the translated text, so a match follows the language", () => {
    const english = en as Record<string, string>;
    const hits = searchSettingsFields("broker", (k) => english[k] ?? k);
    expect(hits.map((h) => h.key)).toContain("set.brokerUrl");
    const german = searchSettingsFields("Kennwort", (k) => (k === "set.password" ? "Kennwort" : english[k] ?? k));
    expect(german.map((h) => h.key)).toEqual(["set.password"]);
  });
});
