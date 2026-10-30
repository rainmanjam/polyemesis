import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { CATALOGS } from "../src/i18n.js";
import { inspectorCatalogSource } from "../scripts/inspectorCatalog.mjs";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");
const COMMITTED = join(ROOT, "com.polyemesis.streamdeck.sdPlugin", "ui", "strings.js");

describe("inspector strings", () => {
  it("matches the catalogs exactly, so the committed file cannot drift from the source", () => {
    expect(readFileSync(COMMITTED, "utf8")).toBe(inspectorCatalogSource());
  });

  it("carries the inspector's keys in every locale, with the same keys as English", () => {
    const source = readFileSync(COMMITTED, "utf8");
    const json = source.slice(source.indexOf("{"), source.lastIndexOf("}") + 1);
    const all = JSON.parse(json) as Record<string, Record<string, string>>;
    expect(Object.keys(all).sort()).toEqual(Object.keys(CATALOGS).sort());
    const enKeys = Object.keys(all.en!).sort();
    // 23 inspector labels (including the preset picker, its placeholder and missing-preset mark, the
    // install-wide note, and the track controls' destination and track pickers with their placeholders,
    // load error, not-listed mark and live-or-restart hint), the poll key's 4 (question, options,
    // duration and hint), 6 connection messages and 4 config errors.
    expect(enKeys).toHaveLength(37);
    for (const [locale, strings] of Object.entries(all)) {
      expect(Object.keys(strings).sort(), locale).toEqual(enKeys);
    }
  });
});
