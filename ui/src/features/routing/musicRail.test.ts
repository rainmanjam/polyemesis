/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { musicRailMark, musicRailReason } from "./musicRail";
import en from "@/lib/i18n/en.json";

describe("musicRailMark", () => {
  it("gives the excluded-music mark a word and a neutral tone", () => {
    // It used to be a bare `text-ok` note glyph with no title, no aria-label
    // and no text, in a rail where green means ON AIR everywhere else on the
    // product — so the mark for "this destination excludes music" read as "this
    // destination is live".
    //
    // It states what the console knows. The setting alone reads as the setting
    // ("music exclusion on"); "music excluded" is said only once a track marked
    // as music is known to be left out, the same rule as the Music rights card.
    expect(musicRailMark(true, false)).toEqual({ label: "route.musicExclusionOn", tone: "muted" });
    expect(musicRailMark(true, true)).toEqual({ label: "route.musicExclusionOn", tone: "muted" });
    expect(musicRailMark(true, false, true)).toEqual({ label: "route.musicExcluded", tone: "muted" });
    expect(musicRailMark(true, true, true)).toEqual({ label: "route.musicExcluded", tone: "muted" });
  });

  it("warns only where the platform's policy and this destination disagree", () => {
    expect(musicRailMark(false, true)).toEqual({ label: "route.railMusicOn", tone: "warn" });
  });

  it("marks nothing when there is nothing to say", () => {
    expect(musicRailMark(false, false)).toBeNull();
  });

  it("uses keys English actually defines, so the rail cannot render a raw key", () => {
    for (const mark of [musicRailMark(true, false), musicRailMark(true, false, true), musicRailMark(false, true)]) {
      expect(en[mark!.label]).toBeTruthy();
    }
  });
});

const ROOT = new URL("../../../../", import.meta.url).pathname;

describe("RoutingPage's destination rail", () => {
  it("draws the mark with its word, not as a bare coloured glyph", () => {
    const src = readFileSync(join(ROOT, "ui/src/pages/RoutingPage.tsx"), "utf8");
    expect(src).toContain("musicRailMark(guarded, pol?.exclude ?? false, excludedNow)");
    expect(src).toContain("{t(mark.label)}");
    // The unlabelled glyphs this replaced. `text-ok` on a note icon, in a
    // rail whose every other green thing means on air.
    expect(src).not.toContain('<Music className="ml-auto h-3 w-3 shrink-0 text-ok" />');
    expect(src).not.toContain('<Music className="ml-auto h-3 w-3 shrink-0 text-warn" />');
  });
});

/* B1: the rail's mark is a word, and the word now carries its reason as well.
 * Every non-null mark has a reason in the catalogue, so a hover or a screen
 * reader never meets a bare "no music" with nothing behind it. */
describe("musicRailReason", () => {
  it("gives every visible mark a reason English defines", () => {
    for (const [guarded, policy] of [[true, false], [true, true], [false, true]] as const) {
      const key = musicRailReason(guarded, policy);
      expect(key, `mark for ${guarded}/${policy} has no reason`).not.toBeNull();
      expect(en[key!]).toBeTruthy();
    }
  });

  it("describes the setting, not an output, until a music track is known to be left out", () => {
    expect(musicRailReason(true, false)).toBe("route.musicTipExcluded");
    expect(musicRailReason(true, false, true)).toBe("foundation.route.noMusicReason");
  });

  it("gives no reason when no mark is shown", () => {
    expect(musicRailReason(false, false)).toBeNull();
  });
});

describe("the rail mark's reason reaches the accessible name, not only a hover title", () => {
  it("renders the reason as screen-reader text inside the destination button", () => {
    // A title attribute is not read by keyboard users and is not part of the
    // button's accessible name. The reason must be text in the button itself.
    const src = readFileSync(join(ROOT, "ui/src/pages/RoutingPage.tsx"), "utf8");
    expect(src).toMatch(/<span className="sr-only">\{markReason \? t\(markReason\) : ""\}<\/span>/);
  });
});

/* The chip is longer than the "no music" it replaced ("music exclusion on"), and
 * in the 16rem rail beside the name it broke every name onto two lines. The name
 * keeps the first line to itself; the chip shares the second line with the
 * routing summary and wraps below it, inside its own box, when it does not fit. */
describe("the destination row's layout", () => {
  const src = readFileSync(join(ROOT, "ui/src/pages/RoutingPage.tsx"), "utf8");
  const row = src.slice(src.indexOf("{list.map(({ destination: d, routing }) => {"), src.indexOf("{/* ---------- editor ---------- */}"));

  it("puts the name alone on the first line, with the wrapping it always had", () => {
    expect(row).toMatch(
      /<span className="flex w-full items-center gap-1 text-\[12px\] font-medium">\s*<span className="min-w-0 break-words">\{d\.name\}<\/span>\s*<\/span>/,
    );
  });

  it("draws the chip after the summary, on the second line, not beside the name", () => {
    expect(row.indexOf("{mark && (")).toBeGreaterThan(row.indexOf("{routing?.summary"));
    expect(row).toContain("flex w-full flex-wrap items-center");
  });

  it("lets the chip wrap inside its own box instead of squeezing or overflowing", () => {
    const chip = row.slice(row.indexOf("{mark && ("));
    expect(chip).toContain("max-w-full");
    expect(chip).not.toMatch(/ml-auto flex shrink-0/);
  });

  it("keeps the chip inside the row's single button, as text and not a focus stop", () => {
    const chip = row.slice(row.indexOf("{mark && ("));
    expect(chip.slice(0, chip.indexOf("</button>"))).not.toMatch(/<(button|a)\b/);
    expect(row.match(/<button\b/g)?.length).toBe(1);
  });
});
