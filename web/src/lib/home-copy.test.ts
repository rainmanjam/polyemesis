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

/* THE HERO (S1). The hero is the one place a reader decides whether to keep
 * reading, so its shape is pinned here: one outcome headline, one subhead
 * sentence, one copy-able install action that is the primary control, the
 * quickstart as a text link, the calculator below the actions, and the real
 * routing product in the first screen. */
describe("the home hero (S1)", () => {
  const home = pages("index.astro");
  const hero = home.slice(home.indexOf("<section class=\"relative overflow-hidden"));
  const heroBody = hero.slice(0, hero.indexOf("</section>"));
  const h1 = heroBody.slice(heroBody.indexOf("<h1"), heroBody.indexOf("</h1>"));
  const sub = heroBody.slice(heroBody.indexOf("rise rise-3"), heroBody.indexOf("</p>", heroBody.indexOf("rise rise-3")));

  it("sets the headline with at most one forced line break", () => {
    expect((h1.match(/<br/g) ?? []).length).toBeLessThanOrEqual(1);
  });

  it("states the subhead in one sentence", () => {
    const text = sub.replace(/<[^>]+>/g, " ");
    const sentences = text.split(/[.!?](\s|$)/).filter((s) => s.trim().length > 0);
    expect(sentences.length).toBe(1);
  });

  it("makes copying the install command the primary action", () => {
    expect(heroBody).toMatch(/btn btn-primary[^>]*data-copy-target="install-command"/);
    expect(heroBody).toMatch(/Copy install command/);
  });

  it("names the install command's code element for that copy action", () => {
    expect(heroBody).toMatch(/codeId="install-command"/);
  });

  it("keeps the quickstart as a text link, not a second button", () => {
    const anchor = /<a\s+href="\/docs\/quickstart"[^>]*>/.exec(heroBody);
    expect(anchor).not.toBeNull();
    expect(anchor![0]).not.toMatch(/btn-primary|btn-ghost/);
  });

  it("puts the calculator link below the primary action", () => {
    expect(heroBody.indexOf("Copy install command")).toBeGreaterThan(-1);
    expect(heroBody.indexOf('href="/calculator"')).toBeGreaterThan(heroBody.indexOf("Copy install command"));
  });

  it("cuts the install caveat out of the hero, leaving the quickstart to carry it", () => {
    // The docker command itself legitimately contains /udp; the caveat prose
    // about it does not belong in the hero any more.
    expect(heroBody).not.toMatch(/SRT is UDP/);
    expect(heroBody).not.toMatch(/1935 is published/);
  });

  it("shows the real routing product in the first screen", () => {
    expect(heroBody).toMatch(/src=\{routing\}/);
  });
});
