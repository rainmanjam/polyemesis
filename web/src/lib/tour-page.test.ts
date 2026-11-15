import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { BY_SLUG } from "../data/docs.mjs";
import { TOUR } from "../data/tour.mjs";
import { TOUR_PAGE, TOUR_ROUTE } from "../data/tour-page.mjs";
import { factProblems, type Fact } from "./facts";

const here = (rel: string) => fileURLToPath(new URL(rel, import.meta.url));
const readDoc = (slug: string): string | undefined => {
  const doc = BY_SLUG.get(slug);
  return doc ? readFileSync(here(`../../../docs/${doc.file}`), "utf8") : undefined;
};
const page = readFileSync(here("../pages/tour.astro"), "utf8");
const notes = TOUR_PAGE.notes as { facts: Fact[]; film: string[]; docs: string[] }[];
const tourBlock = readFileSync(here("../components/home/Tour.astro"), "utf8");

describe("TOUR_PAGE", () => {
  it("is the /tour page, filmed on 0.18.0", () => {
    expect(TOUR_ROUTE).toBe("/tour");
    expect(TOUR_PAGE.filmedVersion).toBe("0.18.0");
  });

  it("has notes for every chapter of the film (a new chapter without notes fails)", () => {
    expect(notes).toHaveLength(TOUR.chapters.length);
  });

  it("has two or three facts per chapter", () => {
    for (const n of notes) {
      expect(n.facts.length).toBeGreaterThanOrEqual(2);
      expect(n.facts.length).toBeLessThanOrEqual(3);
    }
  });

  it("ties every fact to the doc it comes from", () => {
    expect(factProblems(notes.flatMap((n) => n.facts), readDoc)).toEqual([]);
  });

  it("links only published docs", () => {
    for (const n of notes) for (const slug of n.docs) expect(BY_SLUG.has(slug), slug).toBe(true);
  });

  it("does not say what the film cannot show", () => {
    const text = JSON.stringify(notes);
    expect(text).not.toMatch(/real (platform|channel)|connected a|goes? live on youtube/i);
  });
});

describe("the film sentences (P4-R15)", () => {
  it("every chapter has some, each saying it is what the film shows", () => {
    for (const n of notes) {
      expect(n.film.length).toBeGreaterThanOrEqual(1);
      for (const f of n.film) expect(f, f).toMatch(/^(On screen,|The caption reads ")/);
    }
  });

  it("link no doc and name no product capability by citing one", () => {
    for (const n of notes) for (const f of n.film) expect(f).not.toMatch(/\/docs|https?:|\.md\b/);
  });

  it("say that chapter 3's rows reach Live, not only that a Go live sheet appears, and not that they do so one by one", () => {
    // The film starts two rows at once (English and Español are Starting together), so no order is claimed.
    const sentence = notes[2].film.find((f) => /\bLive\b/.test(f) && !/Go live sheet/.test(f));
    expect(sentence).toMatch(/the rows then move from Not started to Starting to Live/);
    expect(sentence).not.toMatch(/one after another|in turn|one by one|in sequence/i);
  });

  it("claim no more of chapter 3 than two Routing pages (the film opens two destinations)", () => {
    for (const f of notes[2].film) {
      expect(f, f).not.toMatch(/each destination/i);
      expect(f, f).not.toMatch(/together/i);
    }
    const readout = notes[2].film.find((f) => /after routing/.test(f));
    expect(readout, "the loudness readout sentence").toMatch(/two of the five/);
    expect(readout).not.toMatch(/\b(all|every|each of the) (five|destination)/i);
  });

  it("quote a caption exactly as the film shows it", () => {
    const captions = notes.flatMap((n) => n.film).filter((f) => f.startsWith("The caption reads"));
    expect(captions).toContain('The caption reads "One click starts all five."');
  });
});

describe("what the page says about the film", () => {
  it("says the destinations are a demo, as docs/media/README.md describes the rig (P4-R16)", () => {
    expect(page).toContain(
      "The tour was filmed on a demo install: its five destinations are labelled as YouTube channels and publish to a local receiver, not to YouTube.",
    );
  });

  it("does not claim the chapter text repeats the captions", () => {
    expect(page).not.toContain("say the same in text");
    expect(page).toContain("the sections below describe each chapter in text");
  });

  it("keeps the player's own attributes", () => {
    for (const s of ["controls", 'preload="none"', "poster={TOUR.poster}"]) expect(tourBlock).toContain(s);
  });

  it("leaves the player's wrap out inside the page column, and keeps it by default", () => {
    expect(tourBlock).toContain('wrap ? "wrap tour" : "tour"');
    expect(tourBlock).toMatch(/wrap = true/);
    expect(page).toContain("<Tour wrap={false} />");
  });
});

describe("tour.astro", () => {
  it("is built from the tour component, the markup helper and the release reader", () => {
    for (const name of ["Tour", "videoLd", "filmedRelease"]) {
      expect(page, name).toMatch(new RegExp(`import[^;]*\\b${name}\\b`));
    }
  });

  it("shows the filmed release, the chapter starts and the markup", () => {
    for (const s of ["Filmed on v", "chapter-", "clock(", "application/ld+json"]) expect(page).toContain(s);
  });

  it("has no page-level style and reads no clock", () => {
    expect(page).not.toContain("<style");
    expect(page).not.toContain("new Date(");
  });

  it("has the sections in order: watch, one per chapter, next", () => {
    expect(page).toContain('id="watch"');
    expect(page).toContain("chapter-${i + 1}");
    expect(page).toContain('id="next"');
  });
});
