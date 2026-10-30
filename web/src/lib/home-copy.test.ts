import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

import { describe, expect, it } from "vitest";

/* Source-level guards for copy decisions that a rendered page cannot be
 * trusted to keep. They live in src/lib, not beside the page: Astro treats
 * every file under src/pages as a route (see features-shots.test.ts). */

const here = dirname(fileURLToPath(import.meta.url));
const pages = (name: string) => readFileSync(join(here, "..", "pages", name), "utf8");

describe("the home page's track layout (S7)", () => {
  const home = pages("index.astro");

  /* The Proof capture numbers its tracks differently from the OBS layout in
   * the problem section. The fix is one layout per place it is used, with the
   * capture's own legend stated once in a single line rather than a paragraph
   * of disclaimer. */
  it("states the capture's track layout in one legend line", () => {
    const legends = home.match(/Track layout in this capture/g) ?? [];
    expect(legends.length).toBe(1);
  });

  it("does not end the payoff paragraph with the Twitch GPU requirement", () => {
    const payoff = home.slice(home.indexOf("polyemesis ingests once and lets you pick"));
    const paragraph = payoff.slice(0, payoff.indexOf("</p>"));
    expect(paragraph).not.toMatch(/needs a supported GPU/);
    expect(paragraph).not.toMatch(/Enhanced Broadcasting/);
  });

  it("links the Twitch GPU detail to the Twitch route page", () => {
    expect(home).toMatch(/href="\/twitch-vod-track"/);
  });
});

describe("time-to-first-stream copy (S2)", () => {
  /* One claim cannot be used everywhere while its number is unmeasured. The
   * fix drops the number and states the three real steps. comparison.astro
   * is excluded on purpose: it describes rival tools, not polyemesis. */
  const own = ["index.astro", "download.astro", "features.astro"];

  it("states no minute figure for polyemesis's own first stream", () => {
    for (const name of own) {
      const src = pages(name);
      expect(src, name).not.toMatch(/(first stream|See it running)[^.<]*(five|two) minutes/i);
      expect(src, name).not.toMatch(/about (five|two) minutes/i);
    }
  });
});
