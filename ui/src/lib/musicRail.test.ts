/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { musicRailMark } from "./musicRail";
import en from "./i18n/en.json";

describe("musicRailMark", () => {
  it("gives the excluded-music mark a word and a neutral tone", () => {
    // It used to be a bare `text-live` note glyph with no title, no aria-label
    // and no text, in a rail where green means ON AIR everywhere else on the
    // product — so the mark for "this destination excludes music" read as "this
    // destination is live".
    expect(musicRailMark(true, false)).toEqual({ label: "route.railNoMusic", tone: "muted" });
    expect(musicRailMark(true, true)).toEqual({ label: "route.railNoMusic", tone: "muted" });
  });

  it("warns only where the platform's policy and this destination disagree", () => {
    expect(musicRailMark(false, true)).toEqual({ label: "route.railMusicOn", tone: "warn" });
  });

  it("marks nothing when there is nothing to say", () => {
    expect(musicRailMark(false, false)).toBeNull();
  });

  it("uses keys English actually defines, so the rail cannot render a raw key", () => {
    for (const mark of [musicRailMark(true, false), musicRailMark(false, true)]) {
      expect(en[mark!.label]).toBeTruthy();
    }
  });
});

const ROOT = new URL("../../../", import.meta.url).pathname;

describe("RoutingPage's destination rail", () => {
  it("draws the mark with its word, not as a bare coloured glyph", () => {
    const src = readFileSync(join(ROOT, "ui/src/pages/RoutingPage.tsx"), "utf8");
    expect(src).toContain("musicRailMark(guarded, pol?.exclude ?? false)");
    expect(src).toContain("{t(mark.label)}");
    // The unlabelled glyphs this replaced. `text-live` on a note icon, in a
    // rail whose every other green thing means on air.
    expect(src).not.toContain('<Music className="ml-auto h-3 w-3 shrink-0 text-live" />');
    expect(src).not.toContain('<Music className="ml-auto h-3 w-3 shrink-0 text-warn" />');
  });
});
