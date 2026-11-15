import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { AUDIENCES } from "../data/audiences.mjs";
import { BY_SLUG } from "../data/docs.mjs";
import * as SETUP from "../data/setup-facts.mjs";
import { RECIPES, RECIPE_ROUTES } from "../data/recipes.mjs";
import { factProblems, type Fact } from "./facts";

const readDoc = (slug: string): string | undefined => {
  const doc = BY_SLUG.get(slug);
  if (!doc) return undefined;
  return readFileSync(fileURLToPath(new URL(`../../../docs/${doc.file}`, import.meta.url)), "utf8");
};
const shotsDir = fileURLToPath(new URL("../assets/shots/", import.meta.url));
const SLUGS = ["two-languages-one-broadcast", "obs-to-five-platforms", "streaming-from-a-backpack"];

type R = (typeof RECIPES)[number];
const factsOf = (r: R): Fact[] => [...r.needs, ...r.steps.flatMap((s: { facts: Fact[] }) => s.facts), ...r.check];
const stripEnd = (s: string) => s.replace(/<[^>]*>/g, "").replace(/\.\s*$/, "").trim();

describe("recipe data", () => {
  it("has the three recipes, in the homepage's order, at /recipes/<slug>", () => {
    expect(RECIPES.map((r: R) => r.slug)).toEqual(SLUGS);
    for (const r of RECIPES) expect(r.route).toBe(`/recipes/${r.slug}`);
    expect(RECIPE_ROUTES).toEqual(Object.fromEntries(SLUGS.map((s) => [s, `/recipes/${s}`])));
  });

  it("has exactly four steps each, and facts in every step", () => {
    for (const r of RECIPES) {
      expect(r.steps, r.slug).toHaveLength(4);
      for (const s of r.steps) expect(s.facts.length).toBeGreaterThanOrEqual(1);
      expect(r.needs.length).toBeGreaterThanOrEqual(1);
      expect(r.check.length).toBeGreaterThanOrEqual(1);
    }
  });

  it("ties every fact to its doc", () => {
    for (const r of RECIPES) expect(factProblems(factsOf(r), readDoc), r.slug).toEqual([]);
  });

  it("uses the homepage's step titles, in order", () => {
    const use = readFileSync(fileURLToPath(new URL("../components/home/Use.astro", import.meta.url)), "utf8");
    for (const r of RECIPES) {
      let at = use.indexOf(`"${stripEnd(r.heading)}"`);
      expect(at, `${r.slug} on the homepage`).toBeGreaterThan(-1);
      for (const s of r.steps) {
        const next = use.indexOf(`"${stripEnd(s.title)}.`, at);
        expect(next, `${r.slug}: ${s.title}`).toBeGreaterThan(-1);
        at = next;
      }
    }
  });

  it("uses shots that exist", () => {
    for (const r of RECIPES) {
      for (const s of r.steps as { shot?: { file: string; alt: string } }[]) {
        if (!s.shot) continue;
        expect(existsSync(shotsDir + s.shot.file), s.shot.file).toBe(true);
        expect(s.shot.alt.length).toBeGreaterThan(10);
      }
    }
  });

  it("links only published docs", () => {
    for (const r of RECIPES) {
      expect(r.docs.length).toBeGreaterThanOrEqual(1);
      for (const slug of r.docs) expect(BY_SLUG.has(slug), slug).toBe(true);
    }
  });

  it("is what every audience page's related list points at", () => {
    for (const a of Object.values(AUDIENCES) as { related: string[] }[]) {
      for (const slug of a.related) expect(SLUGS).toContain(slug);
    }
  });

  it("keeps titles within 60 characters, descriptions within 160, all unique", () => {
    const all = RECIPES.flatMap((r: R) => [r.title, r.description]);
    for (const r of RECIPES) {
      expect(r.title.length).toBeLessThanOrEqual(60);
      expect(r.description.length).toBeLessThanOrEqual(160);
    }
    expect(new Set(all).size).toBe(all.length);
  });

  it("has one h1 heading with an accent that occurs in it", () => {
    for (const r of RECIPES) expect(r.heading.toLowerCase()).toContain(r.accent.toLowerCase());
  });

  it("avoids the banned words", () => {
    const text = JSON.stringify(RECIPES);
    expect(text).not.toMatch(/bypass|dodge|DMCA|pre-release|Stream Deck\+|minute|totalTime|\b\d+\s+stars?\b/i);
  });

  it("says Facebook is still being verified in the platforms recipe", () => {
    const r = RECIPES.find((x: R) => x.slug === "obs-to-five-platforms");
    const text = r!.steps.flatMap((s: { facts: Fact[] }) => s.facts.map((f) => f.text)).join(" ");
    expect(text).toContain("Facebook is still being verified");
  });

  it("does not use the doc's old wording for RTMP", () => {
    const text = JSON.stringify(RECIPES);
    expect(text).not.toMatch(/standard RTMP/i);
    expect(text).toContain("Classic RTMP carries a single track");
  });

  /* Completeness guards: each pins a gap the review found (a recipe whose facts
   * were all true but which left out a step the docs require). */
  describe("completeness", () => {
    const texts = (r: R): string[] => r.steps.flatMap((s: { facts: Fact[] }) => s.facts.map((f) => f.text));
    const by = (slug: string): R => RECIPES.find((r: R) => r.slug === slug)!;
    const at = (list: string[], re: RegExp) => list.findIndex((t) => re.test(t));

    it("gives OBS an address before it is told to start, in every recipe that starts OBS", () => {
      for (const r of RECIPES) {
        const t = texts(r);
        const start = at(t, /Start Recording/);
        if (start < 0) continue;
        const url = at(t, /publish URL/);
        expect(url, `${r.slug}: a fact about the publish URL`).toBeGreaterThan(-1);
        expect(url, `${r.slug}: the publish URL comes before Start Recording`).toBeLessThan(start);
        expect(at(t, /Custom Output \(FFmpeg\)/), `${r.slug}: Custom Output (FFmpeg)`).toBeGreaterThan(-1);
        // Each fact the reader needs to reach "Start Recording" is there, and in the order the docs give.
        const addSource = at(t, /add a source/i);
        expect(addSource, `${r.slug}: adds a source`).toBeGreaterThan(-1);
        expect(t[addSource], `${r.slug}: sets the source to SRT`).toMatch(/SRT/);
        expect(addSource, `${r.slug}: source before its URL`).toBeLessThan(url);
        const paste = at(t, /File path or URL/);
        expect(paste, `${r.slug}: pastes the URL into OBS`).toBeGreaterThan(-1);
        expect(paste, `${r.slug}: pastes before Start Recording`).toBeLessThan(start);
        if (r.slug === "two-languages-one-broadcast") {
          const tracks = at(t, /Audio Track/);
          expect(tracks, `${r.slug}: ticks Audio Track`).toBeGreaterThan(-1);
          expect(tracks, `${r.slug}: ticks before Start Recording`).toBeLessThan(start);
        }
      }
    });

    it("tells the platforms recipe the redirect URL and the hostname", () => {
      const r = by("obs-to-five-platforms");
      expect(texts(r).join(" ")).toMatch(/redirect URL/);
      expect(r.needs.map((f: Fact) => f.text).join(" ")).toMatch(/hostname/);
    });

    it("enables a destination after adding it, in the two recipes that send to destinations", () => {
      for (const slug of ["two-languages-one-broadcast", "obs-to-five-platforms"]) {
        const t = texts(by(slug));
        const add = at(t, /Destinations → Add/);
        expect(add, `${slug}: adds a destination`).toBeGreaterThan(-1);
        const enable = t.findIndex((x, i) => i >= add && /enable/i.test(x));
        expect(enable, `${slug}: a fact that enables it, at or after the add`).toBeGreaterThanOrEqual(add);
      }
    });

    it("picks the connected account before it enables the destination", () => {
      const step = by("obs-to-five-platforms").steps[3].facts.map((f: Fact) => f.text);
      const account = step.findIndex((x: string) => /connected account/i.test(x));
      const enable = step.findIndex((x: string) => /enable/i.test(x));
      expect(by("obs-to-five-platforms").steps[3].facts, "the shared pick-the-account fact").toContain(SETUP.PICK_ACCOUNT);
      expect(step.findIndex((x: string) => /Destinations → Add/.test(x)), "add first").toBeLessThan(account);
      expect(account, "a fact that picks the connected account").toBeGreaterThan(-1);
      expect(enable, "a fact that enables the destination").toBeGreaterThan(-1);
      expect(account).toBeLessThan(enable);
    });

    it("does not leave the platforms recipe saying that registering an app is the same everywhere", () => {
      const step = by("obs-to-five-platforms").steps[2].facts.map((f: Fact) => f.text);
      const form = step.findIndex((x: string) => /redirect URL has the same form/.test(x));
      const register = step.findIndex((x: string) => /Register an app/.test(x));
      expect(form, "states the redirect URL's form").toBeGreaterThan(-1);
      expect(form, "the form comes before the sentence that registers with it").toBeLessThan(register);
      const all = step.join(" ");
      expect(all).toMatch(/its own requirements/);
      expect(all).toMatch(/test user/);
      expect(all).toMatch(/scopes/);
    });

    it("says OBS's Audio tab is in Advanced mode, in the first step of the languages recipe", () => {
      expect(by("two-languages-one-broadcast").steps[0].facts[0].text).toMatch(/Advanced output mode/);
    });

    it("says the backpack source's protocol must be SRT, and cites the SRTLA guide for it", () => {
      const needs = by("streaming-from-a-backpack").needs as Fact[];
      const f = needs.find((n) => /protocol is SRT/.test(n.text));
      expect(f, "a need that states the protocol").toBeDefined();
      expect(f!.doc).toBe("srtla");
      expect(f!.anchor).toBe("setting-it-up");
      expect(f!.evidence).toContain("admits a publish only for a source set to SRT");
    });

    it("checks the languages recipe by its own tracks, not by a music track and a clean track", () => {
      const check = by("two-languages-one-broadcast").check.map((f: Fact) => f.text).join(" ");
      expect(check).not.toMatch(/music|clean track/i);
      expect(check).toMatch(/Audio meters/);
      expect(check).toMatch(/track count/);
    });

    it("says on the backpack page that failover, recording and destinations work the same (the homepage says so)", () => {
      const t = by("streaming-from-a-backpack").check.map((f: Fact) => f.text).join(" ");
      expect(t).toMatch(/ordinary SRT publish/);
      expect(t).toMatch(/failover, recording, destinations and degraded-link failover work the same/i);
    });

    it("keeps to the docs' words about the publish URL", () => {
      expect(JSON.stringify(RECIPES)).not.toMatch(/keep it private/i);
    });

    it("labels the platforms recipe's picture as five YouTube destinations", () => {
      const shot = by("obs-to-five-platforms").steps[3].shot!;
      expect(shot.caption).toMatch(/^Demo install: /);
      expect(shot.caption).toMatch(/YouTube/);
      expect(shot.alt).toMatch(/YouTube/);
    });

    it("states the SRTLA compatibility as the doc does, and never says 'works with'", () => {
      const r = by("streaming-from-a-backpack");
      const all = [r.description, r.lede, ...texts(r), ...r.needs.map((f: Fact) => f.text), ...r.check.map((f: Fact) => f.text)].join(" ");
      expect(all).not.toMatch(/works? with/i);
      expect(r.description.length).toBeLessThanOrEqual(160);
      expect(r.description).toMatch(/srtla_send/);
      expect(r.description).toMatch(/not yet|untested/i);
      const stated = [...r.needs, ...r.check].map((f: Fact) => f.text).join(" ");
      expect(stated).toMatch(/not been tested/);
    });

    it("says where the SRTLA publish token comes from and that the port is a firewall matter", () => {
      const t = texts(by("streaming-from-a-backpack")).join(" ");
      expect(t).toMatch(/source card/);
      expect(t).toMatch(/firewall/);
    });
  });
});
