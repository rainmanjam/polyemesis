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
    const paragraph = payoff.slice(0, payoff.indexOf("</p>")).replace(/<[^>]+>/g, "");
    expect(paragraph).not.toMatch(/GPU/);
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

  /* A step count is also a claim. docs/QUICKSTART.md has eight numbered steps,
   * so "three steps" was false the moment it was written. Say what happens,
   * not how many steps it takes. */
  it("states no step count for the quickstart", () => {
    for (const name of own) {
      expect(pages(name), name).not.toMatch(/three steps/i);
    }
  });

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

/* THE PLAN STRIP (S3). Every figure here must trace to the dated evidence
 * file, and scripts/check-build.mjs requires a page that cites that file to
 * show a check date the file itself claims. The two Restream qualifiers the
 * file records as UNSUPPORTED ("personal profiles only", "excludes Facebook
 * Pages") must not be published, and the word "watermark" is the page's own
 * word for Restream's branding, not the evidence file's. */
describe("the home plan strip (S3)", () => {
  const source = pages("index.astro");
  // Only what renders: the explanatory comments legitimately quote the words
  // the page must not print.
  const home = source.replace(/\{\/\*[\s\S]*?\*\/\}/g, "").replace(/\/\*[\s\S]*?\*\//g, "");

  it("cites the dated competitor evidence file it sources from", () => {
    expect(source).toMatch(/competitor-facts-2026-08-15\.md/);
  });

  it("states the Restream tier figures the evidence file confirms", () => {
    for (const figure of ["$19/mo", "$49/mo", "$239/mo", "$0"]) {
      expect(home, figure).toContain(figure);
    }
    expect(home).toMatch(/2 channels/);
    expect(home).toMatch(/8 on Business/);
  });

  it("does not publish the unsupported Restream qualifiers", () => {
    expect(home).not.toMatch(/personal profiles/i);
    expect(home).not.toMatch(/Facebook Pages/i);
    expect(home).not.toMatch(/watermark/i);
  });

  it("says plainly that self-hosting needs a server", () => {
    expect(home).toMatch(/server/i);
  });

  it("states that Castr has no free tier, as the evidence file does", () => {
    expect(home).toMatch(/Castr,[^.]*no free tier/);
  });

  it("shows the check date next to the figures", () => {
    expect(home).toMatch(/Checked 16 September 2026/);
  });
});

/* THE BUILD-TIME TRUST SIGNALS (S4). The star count and release come from
 * GitHub at build time and are printed only when that read succeeded. A
 * literal count in the source would be a fabricated figure that goes stale. */
describe("the home trust line (S4)", () => {
  const source = pages("index.astro");

  it("reads the repository figures at build time through the helper", () => {
    expect(source).toMatch(/import \{ fetchRepoStats \} from "\.\.\/lib\/github-stats"/);
    expect(source).toMatch(/await fetchRepoStats\(\)/);
  });

  it("prints a star count only from the fetched value, never a typed number", () => {
    expect(source).not.toMatch(/\b\d+\s+stars?\b/);
    expect(source).toMatch(/\{repo\s*&&\s*\(?[\s\S]*?repo\.stars/);
  });

  it("keeps a plain source link when the fetch fails", () => {
    expect(source).toMatch(/Read the source →/);
  });
});

/* THE USE-CASE ROOMS (S6). Each room names the tracks and where each one goes,
 * as a small diagram rather than prose alone. Sports and esports are added
 * because they are real routing cases the product already supports. No
 * dedicated pages are built: features.astro explains why, and that stays. */
describe("the features page rooms (S6)", () => {
  const features = pages("features.astro");

  it("gives each of the five rooms its own routing table", () => {
    // The rooms are one data array rendered by a map, so the count is the
    // array's, and the markup must render that array as a table per room.
    const data = features.slice(features.indexOf("const rooms = ["), features.indexOf("\n];", features.indexOf("const rooms = [")));
    expect((data.match(/title: "/g) ?? []).length).toBe(5);
    expect(features).toMatch(/data-room-routing/);
  });

  it("adds the sports and esports rooms", () => {
    expect(features).toMatch(/Home and away/);
    expect(features).toMatch(/Esports|esports/);
  });

  it("keeps the no-dedicated-pages decision in the source", () => {
    expect(features).toMatch(/PROSE, NOT PAGES/);
  });
});
