import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

import { describe, expect, it } from "vitest";

import { BY_SLUG } from "../data/docs.mjs";
import {
  ALSO,
  CHAT_FACTS,
  DECK_FACTS,
  FAILOVER_FACTS,
  INPUTS,
  LEAD,
  RECORDING_VERBS,
  ROOMS,
  SCREENS,
} from "../data/features.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const read = (...p: string[]) => readFileSync(join(here, ...p), "utf8");
const dataSource = read("..", "data", "features.mjs");
const everything = JSON.stringify({ SCREENS, LEAD, ROOMS, INPUTS, FAILOVER_FACTS, CHAT_FACTS, RECORDING_VERBS, DECK_FACTS, ALSO });

describe("the features data", () => {
  it("has four screenshot sections, each with its own picture and alt text", () => {
    expect(SCREENS.map((s) => s.id)).toEqual(["audio", "inputs", "chat", "recordings"]);
    expect(SCREENS.map((s) => s.shot)).toEqual(["02-routing.png", "05-sources.png", "09-chat.png", "15-recordings.png"]);
    expect(new Set(SCREENS.map((s) => s.shot)).size).toBe(4);
    expect(new Set(SCREENS.map((s) => s.alt)).size).toBe(4);
  });

  it("leads with the per-destination mix", () => {
    expect(LEAD.heading).toBe("Every destination, its own mix.");
    expect(LEAD.heading).toContain(LEAD.accent);
    expect(LEAD.body).toContain(
      "polyemesis ingests once and lets you pick, per destination, which tracks get summed into the stereo feed that destination receives.",
    );
  });

  it("has the input table with its header row and three inputs", () => {
    expect(INPUTS[0]).toEqual(["Input", "Audio tracks", "Good for"]);
    expect(INPUTS.slice(1).map((r) => r[0])).toEqual(["SRT", "RTMP", "SRTLA"]);
    for (const row of INPUTS) expect(row).toHaveLength(3);
  });

  it("has four recording verbs and a non-empty list for each fact set", () => {
    expect(RECORDING_VERBS.map((v) => v.verb)).toEqual(["Record", "Offload", "Transcribe", "Clip"]);
    for (const list of [FAILOVER_FACTS, CHAT_FACTS, DECK_FACTS]) expect(list.length).toBeGreaterThan(0);
  });

  it("states the Stream Deck requirements the doc states", () => {
    const deck = DECK_FACTS.join("\n");
    const doc = read("..", "..", "..", "docs", "STREAM-DECK.md");
    for (const needle of ["7.1", "macOS 12", "Windows 10", "0.12.0"]) {
      expect(deck, needle).toContain(needle);
      expect(doc, needle).toContain(needle);
    }
    expect(deck).toMatch(/eight keys, a touch strip and four dials/);
  });

  it("states the failover thresholds the doc gives as defaults", () => {
    const f = FAILOVER_FACTS.join("\n");
    const doc = read("..", "..", "..", "docs", "LINK-HEALTH.md");
    for (const n of ["800", "1500", "15"]) {
      expect(f, n).toContain(n);
      expect(doc, n).toContain(n);
    }
  });
});

describe("the features page rooms (S6)", () => {
  it("gives each of the five rooms a title and routes", () => {
    expect(ROOMS).toHaveLength(5);
    for (const r of ROOMS) {
      expect(r.title).not.toBe("");
      expect(r.routes.length).toBeGreaterThan(0);
    }
  });

  it("adds the sports and esports rooms", () => {
    const titles = ROOMS.map((r) => r.title).join("|");
    expect(titles).toContain("Home and away");
    expect(titles).toContain("Esports");
  });
});

describe("the also-included groups", () => {
  it("has four groups, every entry linking to a published doc", () => {
    expect(ALSO.map((g) => g.group)).toEqual(["On air", "Platforms getting connected", "Viewers", "Operations"]);
    for (const g of ALSO) {
      expect(g.items.length).toBeGreaterThan(0);
      for (const item of g.items) expect(BY_SLUG.has(item.doc), `${item.name} -> ${item.doc}`).toBe(true);
    }
  });

  it("keeps the sentence the build's catalogue-count rule reads", () => {
    const entry = ALSO.flatMap((g) => g.items).find((i) => /RTMP, RTMPS or SRT/.test(i.name));
    expect(entry?.text).toMatch(/\w+(-\w+)? other presets in the catalogue/);
  });

  /* #924 took "uptime" out of the copy because it read "-" on the one-port
   * ingest; the relay hub now times the stream, so the copy says it again. */
  it("claims the ingest uptime the dashboard card shows on every ingest mode", () => {
    const dash = ALSO.flatMap((g) => g.items).find((i) => i.name === "Dashboard");
    expect(dash?.text).toMatch(/its ingest \(bitrate, uptime,/);
  });
});

describe("the features copy rules", () => {
  it.each(["Stream Deck+", "bypass", "dodge", "DMCA", "pre-release", "one maintainer"])("data never says %s", (w) => {
    expect(dataSource).not.toContain(w);
    expect(everything).not.toContain(w);
  });

  it("the data never names the music track or leads with music", () => {
    expect(dataSource).not.toContain("MUSIC_TRACK");
    expect(dataSource).not.toContain("YouTube gets everything but track");
  });

  // Page-source case: red until the page is rebuilt on this data (Task 13).
  it("the page never names the music track or leads with music", () => {
    const page = read("..", "pages", "features.astro");
    expect(page).not.toContain("MUSIC_TRACK");
    expect(page).not.toContain("YouTube gets everything but track");
  });
});
