/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";

/* The Settings screen tells the operator how many audio tracks SRT carries.
 * That number once said "six" (OBS's limit) long after routing.MaxTracks became
 * 32, and nothing failed. This pins the copy, in every locale, to the Go
 * constant, which is the number the server actually enforces.
 */

const ROOT = new URL("../../../", import.meta.url).pathname;
const LOCALES = new URL("./i18n/", import.meta.url).pathname;

const goSource = readFileSync(join(ROOT, "internal/routing/profile.go"), "utf8");
const maxTracks = Number(/^const MaxTracks = (\d+)/m.exec(goSource)?.[1]);

describe("Settings ingest copy track limit", () => {
  it("reads MaxTracks from the Go source", () => {
    expect(Number.isInteger(maxTracks) && maxTracks > 0).toBe(true);
  });

  for (const file of readdirSync(LOCALES).filter((f) => f.endsWith(".json"))) {
    it(`${file} names the real limit in set.ingestDesc`, () => {
      const msgs = JSON.parse(readFileSync(join(LOCALES, file), "utf8")) as Record<string, string>;
      const text = msgs["set.ingestDesc"];
      expect(text, "set.ingestDesc missing").toBeTruthy();
      const nums = (text.match(/\d+/g) ?? []).map(Number);
      expect(nums).toEqual([maxTracks]);
    });
  }
});
