import { describe, expect, it } from "vitest";

import { CASE_ORDER, CASES, mixLabel } from "../data/cases.mjs";

describe("example cases", () => {
  it("lists the cases in display order", () => expect(CASE_ORDER).toEqual(["sports", "worship", "streamer"]));

  it("has a case for every key in the order", () => {
    for (const key of CASE_ORDER) expect(CASES[key].key).toBe(key);
  });

  it("gives each sports destination its own language and the crowd", () => {
    expect(CASES.sports.dests).toHaveLength(5);
    for (const d of CASES.sports.dests) {
      const language = d.name.split(" · ")[1]
        .normalize("NFD").replace(/[̀-ͯ]/g, "").toLowerCase();
      expect(d.tracks).toEqual([language, "crowd"]);
    }
  });

  it("only routes tracks that exist in the case", () => {
    for (const key of CASE_ORDER) {
      const ids = CASES[key].tracks.map((t: { id: string }) => t.id);
      for (const d of CASES[key].dests) for (const t of d.tracks) expect(ids).toContain(t);
    }
  });

  it("keeps each example's fine print", () => {
    for (const key of CASE_ORDER) {
      expect(CASES[key].fine).toMatch(/doesn't license/);
      expect(CASES[key].fine).toContain("Use tracks you have the rights to.");
    }
    expect(CASES.sports.fine.startsWith("Translation comes from your own interpreters")).toBe(true);
    expect(CASES.worship.fine.startsWith("Translation comes from your own interpreters")).toBe(true);
    expect(CASES.streamer.fine).not.toContain("Translation");
  });

  it("places every point on the globe", () => {
    for (const key of CASE_ORDER) {
      const c = CASES[key];
      for (const p of [c.source, ...c.dests]) {
        expect(Math.abs(p.lat)).toBeLessThanOrEqual(90);
        expect(Math.abs(p.lon)).toBeLessThanOrEqual(180);
      }
    }
  });

  it("labels a destination's mix with upper-cased track names", () => {
    expect(mixLabel(CASES.sports, CASES.sports.dests[0])).toBe("video + ENGLISH + CROWD");
    expect(mixLabel(CASES.sports, CASES.sports.dests[1])).toBe("video + ESPAÑOL + CROWD");
  });

  it("a case with more than one audio track comes in over SRT", () => {
    for (const key of CASE_ORDER) {
      const c = CASES[key];
      if (c.tracks.length > 1) {
        expect(c.source.spec).toContain("SRT");
        expect(c.source.spec).not.toContain("RTMP");
      }
    }
  });
});
