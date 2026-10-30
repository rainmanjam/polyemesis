import { describe, expect, it } from "vitest";
import { SETTINGS_TABS, searchSettings } from "@/lib/settingsSearch";

const labels = {
  ingest: "Ingest",
  pipeline: "Pipeline",
  platforms: "Platforms",
  security: "Security",
  debug: "Debug",
} as const;

describe("searchSettings", () => {
  it("returns nothing for an empty or blank query", () => {
    expect(searchSettings("", labels)).toEqual([]);
    expect(searchSettings("   ", labels)).toEqual([]);
  });

  it("matches a section name case-insensitively", () => {
    expect(searchSettings("SECUR", labels)).toEqual(["security"]);
  });

  it("returns every section whose name contains the query, in rail order", () => {
    expect(searchSettings("i", labels)).toEqual(["ingest", "pipeline", "security"]);
  });

  it("matches the translated label, so a non-English install searches in its own words", () => {
    const de = { ...labels, security: "Sicherheit" };
    expect(searchSettings("sicher", de)).toEqual(["security"]);
    expect(searchSettings("security", de)).toEqual([]);
  });

  it("keeps the rail order fixed and covers the five sections the page has", () => {
    expect(SETTINGS_TABS).toEqual(["ingest", "pipeline", "platforms", "security", "debug"]);
  });
});
