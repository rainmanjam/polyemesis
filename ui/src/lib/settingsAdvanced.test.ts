/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { ADVANCED_CARDS, advancedCardIsNonDefault, advancedCardShown, isAdvancedCard } from "./settingsAdvanced";
import { SETTINGS_INDEX } from "./settingsSearch";
import type { Settings } from "./types";

/* THE ADVANCED SWITCH NEVER HIDES A VALUE THE OPERATOR CHOSE (settings-council R7).
 *
 * Each case starts from db.DefaultSettings' values and moves one field. The
 * default must read as default (hidden behind the switch) and the moved field
 * must read as non-default (shown whatever the switch says). */

function defaults(): Settings {
  return {
    preview: { enabled: true, segmentSeconds: 1, videoHeight: 360, videoKbps: 800, idleTimeoutSeconds: 30 },
    meters: { enabled: true, intervalMs: 100 },
    logging: { persistProcessLogs: true, maxFileMb: 8, maxFiles: 3 },
    mqtt: { enabled: false },
  } as unknown as Settings;
}

describe("advancedCardIsNonDefault", () => {
  it("reads every card as default on a fresh install", () => {
    for (const card of ADVANCED_CARDS) {
      expect(advancedCardIsNonDefault(card, defaults()), card).toBe(false);
    }
  });

  it.each([
    ["set.mqtt", (s: Settings): void => {
      s.mqtt = { enabled: true };
    }],
    ["set.mqtt", (s: Settings): void => {
      s.mqtt = { enabled: false, brokerUrl: "mqtts://broker.lan" };
    }],
    ["set.preview", (s: Settings): void => {
      s.preview.enabled = false;
    }],
    ["set.preview", (s: Settings): void => {
      s.preview.videoHeight = 720;
    }],
    ["set.audioMeters", (s: Settings): void => {
      s.meters.intervalMs = 250;
    }],
    ["set.processLogs", (s: Settings): void => {
      s.logging.persistProcessLogs = false;
    }],
    ["set.processLogs", (s: Settings): void => {
      s.logging.maxFiles = 10;
    }],
    ["set.ebTitle", (s: Settings): void => {
      s.multitrack = { gpus: [{ model: "RTX", vendorId: 4318 }] };
    }],
  ] as const)("reads %s as non-default once a field moves", (card, move) => {
    const s = defaults();
    move(s);
    expect(advancedCardIsNonDefault(card, s)).toBe(true);
  });
});

describe("advancedCardShown", () => {
  const base = { show: false, settings: defaults(), inError: false, revealed: new Set<string>() };

  it("hides a default card while the switch is off", () => {
    expect(advancedCardShown("set.preview", base)).toBe(false);
  });

  it("shows it once the switch is on", () => {
    expect(advancedCardShown("set.preview", { ...base, show: true })).toBe(true);
  });

  it("shows every card while the last save is in error, since the refusal may be about any of them", () => {
    expect(advancedCardShown("set.preview", { ...base, inError: true })).toBe(true);
  });

  it("shows a card a search result opened", () => {
    expect(advancedCardShown("set.preview", { ...base, revealed: new Set(["set.preview"]) })).toBe(true);
  });
});

describe("the advanced cards and the page agree", () => {
  const page = readFileSync(fileURLToPath(new URL("../pages/SettingsPage.tsx", import.meta.url)), "utf8");

  it("names only cards the search index knows, so a hit can open them", () => {
    const titles = new Set(SETTINGS_INDEX.filter((e) => !e.card).map((e) => e.key as string));
    for (const card of ADVANCED_CARDS) expect(titles.has(card), card).toBe(true);
    expect(isAdvancedCard("set.failover")).toBe(false);
  });

  it("wraps each advanced card on the page in an AdvancedCard", () => {
    for (const card of ADVANCED_CARDS) {
      expect(page, `${card} is not wrapped in <AdvancedCard card="${card}">`).toContain(`<AdvancedCard card="${card}"`);
    }
  });
});
