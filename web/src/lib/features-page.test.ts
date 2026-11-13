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
