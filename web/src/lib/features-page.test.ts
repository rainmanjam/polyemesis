import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

import { describe, expect, it } from "vitest";

import { LEAD } from "../data/features.mjs";
import { TRACK_LAYOUT } from "../data/tracks.mjs";

/* Source-level guards for the rebuilt /features page. They live in src/lib,
 * not beside the page: Astro treats every file under src/pages as a route. */

const here = dirname(fileURLToPath(import.meta.url));
const features = readFileSync(join(here, "..", "pages", "features.astro"), "utf8");

describe("the features page's track layout", () => {
  it("states the capture's track layout in one legend line", () => {
    expect((features.match(/Track layout in this capture/g) ?? []).length).toBe(1);
  });

  it("reads the track layout from the shared module and no longer imports the patch bay", () => {
    expect(features).toMatch(/from "\.\.\/data\/tracks\.mjs"/);
    expect(features).not.toMatch(/import MixMatrix/);
    expect(features).not.toMatch(/Track 1 is the full mix/i);
  });

  it("matches the layout of the seeded install the screenshots come from", () => {
    // plan 3 Task 11 retargets this to scripts/demo-seed.sports.fixture.json
    const fixture = JSON.parse(
      readFileSync(join(here, "..", "..", "..", "scripts", "demo-seed.fixture.json"), "utf8"),
    ) as { sources: { audio: { label: string }[] }[] };
    const captured = fixture.sources[0].audio.map((a) => a.label);
    expect(
      TRACK_LAYOUT.map((t) => t.fixtureLabel),
      "src/data/tracks.mjs no longer matches the first programme in the demo-seed fixture, so the " +
        "features page names tracks the routing screenshot does not show",
    ).toEqual(captured);
  });

  it("does not end the lead with the Twitch GPU requirement", () => {
    const lead = LEAD.body.join(" ");
    expect(lead).not.toMatch(/GPU/);
    expect(lead).not.toMatch(/Enhanced Broadcasting/);
  });

  it("links the Twitch GPU detail to the Twitch route page", () => {
    expect(features).toMatch(/href="\/twitch-vod-track"/);
  });
});

describe("the features page order", () => {
  it("reads header, then audio, inputs, failover, chat, recordings, deck, also", () => {
    let from = features.indexOf("<InnerPage");
    expect(from, "<InnerPage").toBeGreaterThan(-1);
    for (const id of ["audio", "inputs", "failover", "chat", "recordings", "deck", "also"]) {
      const at = features.indexOf(`id="${id}"`, from);
      expect(at, `section #${id} after the previous one`).toBeGreaterThan(-1);
      from = at;
    }
  });
});

describe("does not carry the old patch bay", () => {
  it.each(["<MixMatrix", "<LoudnessBars", "<SpecStrip", "hosted plans", "Checked 16 September 2026"])(
    "page source has no %s",
    (s) => expect(features).not.toContain(s),
  );

  it.each(['href="/#matrix"', 'href="/#deck"'])("page source links %s", (s) => expect(features).toContain(s));
});

/* A section's eyebrow must not repeat its own title (2026-11 design review;
 * the old `{s.eyebrow}` guard from home-copy.test.ts, adapted to the new shape). */
describe("the features page does not repeat a title as its eyebrow", () => {
  const norm = (t: string) => t.replace(/<[^>]+>/g, "").replace(/[^a-z0-9 ]/gi, "").replace(/\s+/g, " ").trim().toLowerCase();

  it("the lead's eyebrow is not its heading, and its heading does not contain it", () => {
    expect(norm(LEAD.eyebrow)).not.toBe(norm(LEAD.heading));
    expect(norm(LEAD.heading)).not.toContain(norm(LEAD.eyebrow));
  });

  // Page-source case: green on the old page by absence; it guards Task 13's rebuild.
  it("no section's eyebrow text equals its h2 text", () => {
    const eyebrows = [...features.matchAll(/<p class="eyebrow">([^<{]+)<\/p>/g)].map((m) => norm(m[1]));
    const h2s = [...features.matchAll(/<h2[^>]*>([\s\S]*?)<\/h2>/g)].map((m) => norm(m[1]));
    for (const e of eyebrows) expect(h2s, `eyebrow "${e}" repeats an h2`).not.toContain(e);
    expect(features).not.toMatch(/<p class="eyebrow">\{s\.eyebrow\}<\/p>/);
  });
});

describe("the rebuilt features page's sections", () => {
  const layouts = ["audio", "inputs", "failover", "chat", "recordings"].map((id) => {
    const m = new RegExp(`<section id="${id}"[^>]*data-layout="([^"]+)"`).exec(features);
    return m?.[1];
  });

  it("gives the five in-depth sections five distinct data-layout values", () => {
    expect(layouts.every(Boolean)).toBe(true);
    expect(new Set(layouts).size).toBe(5);
  });

  it("has no duplicate literal section id", () => {
    const ids = [...features.matchAll(/<section id="([^"]+)"/g)].map((m) => m[1]);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("uses Shot and never Picture directly", () => {
    expect(features).toMatch(/import Shot from/);
    expect(features).not.toMatch(/\bPicture\b/);
  });

  const deck = /<section id="deck"[\s\S]*?<\/section>/.exec(features)?.[0] ?? "";

  it("carries the credit line and the not-affiliated sentence in the deck block", () => {
    expect(deck).toContain("Stream Deck + model: Elgato");
    expect(deck).toContain("is not affiliated with or endorsed by");
  });

  it("copies the footer's not-affiliated sentence verbatim", () => {
    const footer = readFileSync(join(here, "..", "components", "Footer.astro"), "utf8");
    const sentence = /polyemesis is not affiliated with or endorsed by[^<]*?owners\./.exec(footer)?.[0];
    expect(sentence).toBeTruthy();
    expect(deck.replace(/\s+/g, " ")).toContain(sentence);
  });

  it("uses an Image for the deck, not a Shot, and never writes Stream Deck+", () => {
    expect(deck).not.toContain("<Shot");
    expect(deck).toContain("<Image");
    expect(features).toMatch(/import deckStill from "\.\.\/assets\/home\/deck-still\.jpg"/);
    expect(features).not.toContain("Stream Deck+");
  });
});

describe("FailoverLadder", () => {
  const ladder = readFileSync(join(here, "..", "components", "features", "FailoverLadder.astro"), "utf8");
  it.each(["data-failover", "data-fo-controls", "data-stage", "data-play", "data-frame", "data-enc", "data-dest", "data-log", "data-pause"])(
    "renders %s",
    (attr) => expect(ladder).toContain(attr),
  );
});
