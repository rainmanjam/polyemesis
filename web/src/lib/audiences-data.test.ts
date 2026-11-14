import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { AUDIENCES, AUDIENCE_ROUTES } from "../data/audiences.mjs";
import { CASES, CASE_ORDER } from "../data/cases.mjs";
import { BY_SLUG } from "../data/docs.mjs";
import { factProblems, fillCase, type Fact } from "./facts";

const readDoc = (slug: string): string | undefined => {
  const doc = BY_SLUG.get(slug);
  if (!doc) return undefined;
  return readFileSync(fileURLToPath(new URL(`../../../docs/${doc.file}`, import.meta.url)), "utf8");
};
const shotsDir = fileURLToPath(new URL("../assets/shots/", import.meta.url));

const keys = CASE_ORDER as readonly ("sports" | "worship" | "streamer")[];
const audiences = keys.map((k) => AUDIENCES[k]);
const factsOf = (a: (typeof audiences)[number]): Fact[] => [...a.needs, ...a.steps.flatMap((s: { facts: Fact[] }) => s.facts)];

describe("audience data", () => {
  it("has the three routes and one audience per case, in order", () => {
    expect(AUDIENCE_ROUTES).toEqual({ sports: "/for/sports-clubs", worship: "/for/churches-and-events", streamer: "/for/streamers" });
    expect(Object.keys(AUDIENCES)).toEqual([...CASE_ORDER]);
    for (const k of keys) expect(AUDIENCES[k].route).toBe(AUDIENCE_ROUTES[k]);
  });

  it("ties every fact to its doc", () => {
    for (const a of audiences) expect(factProblems(factsOf(a), readDoc)).toEqual([]);
  });

  it("leaves no token unfilled", () => {
    for (const a of audiences) {
      for (const f of factsOf(a)) expect(fillCase(f.text, CASES[a.key])).not.toContain("{");
      for (const s of a.steps) expect(fillCase(s.title, CASES[a.key])).not.toContain("{");
    }
  });

  it("has steps with facts, and needs that say single track", () => {
    for (const a of audiences) {
      expect(a.steps.length).toBeGreaterThanOrEqual(3);
      for (const s of a.steps) expect(s.facts.length).toBeGreaterThanOrEqual(1);
      expect(a.needs.some((f: Fact) => f.text.includes("single track"))).toBe(true);
    }
  });

  it("names five distinct published docs under features", () => {
    for (const a of audiences) {
      expect(a.features).toHaveLength(5);
      expect(new Set(a.features).size).toBe(5);
      for (const slug of a.features) expect(BY_SLUG.has(slug), slug).toBe(true);
    }
  });

  it("uses shots that exist, once per page, with the demo caption on every destinations picture and off the other sports shots", () => {
    for (const a of audiences) {
      const shots = a.steps.flatMap((s: { shot?: { file: string; caption?: string } }) => (s.shot ? [s.shot] : []));
      for (const sh of shots) {
        expect(existsSync(shotsDir + sh.file), sh.file).toBe(true);
        // The destinations picture is the sports demo install everywhere, so it is always labelled as one.
        if (sh.file === "18-destinations.png") expect(sh.caption, `${a.key} destinations shot`).toMatch(/^Demo install: /);
        else if (a.key === "sports") expect(sh.caption).toBeUndefined();
      }
      const files = shots.map((s: { file: string }) => s.file);
      expect(new Set(files).size).toBe(files.length);
    }
  });

  it("keeps titles within 60 characters, descriptions within 160, all unique", () => {
    const all = audiences.flatMap((a) => [a.title, a.description]);
    for (const a of audiences) {
      expect(a.title.length).toBeLessThanOrEqual(60);
      expect(a.description.length).toBeLessThanOrEqual(160);
    }
    expect(new Set(all).size).toBe(all.length);
  });

  it("carries the case's own fine print", () => {
    for (const k of keys) expect(CASES[k].fine).toMatch(/doesn't license/);
  });

  it("avoids the banned words", () => {
    const text = readFileSync(fileURLToPath(new URL("../data/audiences.mjs", import.meta.url)), "utf8");
    expect(text).not.toMatch(/bypass|dodge|DMCA|pre-release|Stream Deck\+|minute|\b\d+\s+stars?\b/i);
  });

  it("never states a connected account without the stream-key alternative", () => {
    for (const a of audiences) {
      for (const f of factsOf(a)) {
        if (/connected (youtube )?account/i.test(f.text) && f.doc === "quickstart") expect(f.text, f.text).toMatch(/stream key/i);
      }
    }
  });

  it("says no destination the case does not have", () => {
    // A claim about a kind of destination may appear in an audience's own strings only if its case has it.
    const KINDS = [/podcast/i, /speech-only/i, /congregation/i];
    for (const a of audiences) {
      const c = CASES[a.key];
      const caseText = [...c.tracks.map((t: { label: string }) => t.label), ...c.dests.map((d: { name: string }) => d.name)].join(" ");
      const own = [a.title, a.description, a.heading, a.lede, a.band.heading, a.band.lede].join(" ");
      for (const k of KINDS) if (k.test(own)) expect(caseText, `${a.key}: "${k}" is not in its case`).toMatch(k);
    }
  });
});
