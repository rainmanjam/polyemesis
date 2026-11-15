import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { AUDIENCES, AUDIENCE_ROUTES } from "../data/audiences.mjs";
import * as SETUP from "../data/setup-facts.mjs";
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

  it("carries the case's own fine print, and the page renders it from the case", () => {
    for (const k of keys) expect(CASES[k].fine).toMatch(/doesn't license/);
    const layout = readFileSync(fileURLToPath(new URL("../layouts/AudiencePage.astro", import.meta.url)), "utf8");
    expect(layout).toMatch(/<p class="fine" data-fine>\{c\.fine\}<\/p>/);
    expect(layout).toContain("const c = CASES[a.key]");
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

  it("says no destination the case does not have in llms.txt either", () => {
    const KINDS = [/podcast/i, /speech-only/i, /congregation/i];
    const llms = readFileSync(fileURLToPath(new URL("../../public/llms.txt", import.meta.url)), "utf8").split("\n");
    for (const a of audiences) {
      const line = llms.find((l) => l.includes(`https://polyemesis.com${a.route})`));
      expect(line, `${a.key}: its llms.txt line`).toBeDefined();
      const c = CASES[a.key];
      const caseText = [...c.tracks.map((t: { label: string }) => t.label), ...c.dests.map((d: { name: string }) => d.name)].join(" ");
      for (const k of KINDS) if (k.test(line!)) expect(caseText, `${a.key}: llms.txt says "${k}", which its case does not have`).toMatch(k);
    }
  });

  /* Completeness guards (ruling P4-R17): each pins a gap the final review found, a page whose facts were
   * all true but which left out a step the docs require. Each is proved by removing the fact it guards. */
  describe("completeness", () => {
    const texts = (a: (typeof audiences)[number]): string[] => a.steps.flatMap((s: { facts: Fact[] }) => s.facts.map((f) => f.text));
    const at = (list: string[], re: RegExp) => list.findIndex((t) => re.test(t));

    it("gives OBS an address before it is told to start, on every audience page", () => {
      for (const a of audiences) {
        const t = texts(a);
        const start = at(t, /Start Recording/);
        expect(start, `${a.key}: says Start Recording`).toBeGreaterThan(-1);
        const url = at(t, /publish URL/);
        expect(url, `${a.key}: a fact about the publish URL`).toBeGreaterThan(-1);
        expect(url, `${a.key}: the publish URL comes before Start Recording`).toBeLessThan(start);
        const addSource = at(t, /add a source/i);
        expect(addSource, `${a.key}: adds a source`).toBeGreaterThan(-1);
        expect(t[addSource], `${a.key}: sets the source to SRT`).toMatch(/SRT/);
        expect(addSource, `${a.key}: source before its URL`).toBeLessThan(url);
        expect(at(t, /Custom Output \(FFmpeg\)/), `${a.key}: Custom Output (FFmpeg)`).toBeGreaterThan(-1);
        const paste = at(t, /File path or URL/);
        expect(paste, `${a.key}: pastes the URL into OBS`).toBeGreaterThan(-1);
        expect(paste, `${a.key}: pastes before Start Recording`).toBeLessThan(start);
      }
    });

    it("ticks the Audio Track boxes before Start Recording, and sets the track bitrates first", () => {
      for (const a of audiences) {
        const t = texts(a);
        const start = at(t, /Start Recording/);
        const ticks = at(t, /Audio Track/);
        expect(ticks, `${a.key}: ticks Audio Track`).toBeGreaterThan(-1);
        expect(ticks, `${a.key}: ticks before Start Recording`).toBeLessThan(start);
        const bitrates = at(t, /set a bitrate for each track/);
        expect(bitrates, `${a.key}: sets a bitrate for each track`).toBeGreaterThan(-1);
        expect(bitrates, `${a.key}: bitrates before Start Recording`).toBeLessThan(start);
        expect(at(t, /Advanced Audio Properties/), `${a.key}: assigns sources to tracks`).toBeGreaterThan(-1);
      }
    });

    it("adds a destination, ticks its tracks and enables it, in that order", () => {
      for (const a of audiences) {
        const t = texts(a);
        const add = at(t, /Destinations → Add/);
        expect(add, `${a.key}: adds a destination`).toBeGreaterThan(-1);
        const tick = t.findIndex((x, i) => i > add && /tick the ones/i.test(x));
        expect(tick, `${a.key}: ticks its tracks after the add`).toBeGreaterThan(add);
        const enable = t.findIndex((x, i) => i > tick && /\benable\b/i.test(x));
        expect(enable, `${a.key}: enables it after the ticks`).toBeGreaterThan(tick);
      }
    });

    it("says what an account needs before it says to connect one, with the stream key still offered", () => {
      for (const a of audiences) {
        const t = texts(a);
        const host = at(t, /real hostname with a trusted certificate/);
        expect(host, `${a.key}: the hostname and certificate`).toBeGreaterThan(-1);
        expect(t[host], `${a.key}: never its IP`).toMatch(/never its IP/);
        const connect = at(t, /Connect account/);
        expect(connect, `${a.key}: presses Connect account`).toBeGreaterThan(host);
        expect(at(t, /its own requirements/), `${a.key}: each platform's app has its own requirements`).toBeGreaterThan(-1);
        expect(at(t, /redirect URL/), `${a.key}: the redirect URL`).toBeGreaterThan(-1);
        expect(texts(a).join(" "), `${a.key}: the stream-key alternative`).toMatch(/platform's own dashboard/);
      }
    });

    it("uses the facts it shares with the recipes, not its own wording of them", () => {
      const shared = [
        SETUP.OBS_BITRATES, SETUP.OBS_TRACK_TICKS, SETUP.ADD_SOURCE, SETUP.COPY_URL, SETUP.OBS_CUSTOM_OUTPUT, SETUP.PASTE_URL,
        SETUP.AUDIO_TRACKS, SETUP.START_RECORDING, SETUP.LABEL_TRACKS, SETUP.ADD_DESTINATION, SETUP.URL_AND_KEY, SETUP.TICK_TRACKS,
        SETUP.ENABLE_DESTINATION, SETUP.METERS_AFTER_ROUTING, SETUP.SILENCE_FACT, SETUP.CONNECT_NEEDS_HOSTNAME,
        SETUP.APP_REQUIREMENTS, SETUP.REDIRECT_URL, SETUP.REGISTER_APP, SETUP.PASTE_CREDENTIALS, SETUP.SAME_FOUR_STEPS,
      ];
      for (const a of audiences) for (const f of shared) expect(factsOf(a), `${a.key}: ${f.text}`).toContain(f);
    });

    it("does not tell the streamer that every destination receives the encoder's video as it is", () => {
      // A rendition re-encodes the vertical destination's video, so the sentence is true on the other two pages only.
      expect(factsOf(AUDIENCES.streamer)).not.toContain(SETUP.ENCODER_SETTINGS);
      expect(factsOf(AUDIENCES.sports)).toContain(SETUP.ENCODER_SETTINGS);
      expect(factsOf(AUDIENCES.worship)).toContain(SETUP.ENCODER_SETTINGS);
    });

    it("says where the church's non-OBS encoder stands, since its example source is a hardware encoder", () => {
      expect(factsOf(AUDIENCES.worship)).toContain(SETUP.ANY_SRT_ENCODER);
    });

    it("tells the streamer where to add the rendition, how to shape it and to choose Blurred fill", () => {
      const t = texts(AUDIENCES.streamer).join(" ");
      expect(t).toMatch(/Renditions page/);
      expect(t).toMatch(/both a width and a height/);
      expect(t).toMatch(/Blurred fill/);
      expect(t).toMatch(/Stretch to fit/);
      const adds = at(texts(AUDIENCES.streamer), /Destinations → Add/);
      const rendition = at(texts(AUDIENCES.streamer), /Renditions page/);
      expect(adds, "destinations exist before they are pointed at a rendition").toBeLessThan(rendition);
    });
  });
});
