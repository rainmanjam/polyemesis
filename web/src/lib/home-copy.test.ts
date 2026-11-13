import { readFileSync, readdirSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

import { describe, expect, it } from "vitest";

import { COMPETITORS } from "./competitors";

/* Source-level guards for copy decisions that a rendered page cannot be
 * trusted to keep. They live in src/lib, not beside the page: Astro treats
 * every file under src/pages as a route (see features-shots.test.ts). */

const here = dirname(fileURLToPath(import.meta.url));
const pages = (name: string) => readFileSync(join(here, "..", "pages", name), "utf8");

/* index.astro holds no copy any more: the homepage is the files under
 * components/home/ (plus index.astro, which assembles them). */
const homeDir = join(here, "..", "components", "home");
const homeFile = (name: string) => readFileSync(join(homeDir, name), "utf8");
const homeFiles = (): [name: string, src: string][] => [
  ["index.astro", pages("index.astro")],
  ...readdirSync(homeDir)
    .filter((f) => statSync(join(homeDir, f)).isFile())
    .map((f): [string, string] => [`components/home/${f}`, homeFile(f)]),
];

describe("time-to-first-stream copy (S2)", () => {
  /* One claim cannot be used everywhere while its number is unmeasured. The
   * fix drops the number and states the three real steps. comparison.astro
   * is excluded on purpose: it describes rival tools, not polyemesis. */
  const own = (): [name: string, src: string][] => [
    ...homeFiles(),
    ["download.astro", pages("download.astro")],
    ["features.astro", pages("features.astro")],
  ];

  /* A step count is also a claim. docs/QUICKSTART.md has eight numbered steps,
   * so "three steps" was false the moment it was written. Say what happens,
   * not how many steps it takes. */
  it("states no step count for the quickstart", () => {
    for (const [name, src] of own()) {
      expect(src, name).not.toMatch(/three steps/i);
    }
  });

  it("states no minute figure for polyemesis's own first stream", () => {
    for (const [name, src] of own()) {
      expect(src, name).not.toMatch(/(first stream|See it running)[^.<]*(five|two) minutes/i);
      expect(src, name).not.toMatch(/about (five|two) minutes/i);
    }
  });
});

/* THE HOMEPAGE (2026-11 redesign). index.astro only assembles the sections;
 * the copy lives in components/home/. */
describe("the home page (2026-11 redesign)", () => {
  const index = pages("index.astro");

  const TAGS = [
    "Hero",
    "JourneyBar",
    "HowItWorks",
    "MixMatrixPlay",
    "Audiences",
    "FeatureTour",
    "StreamDeck",
    "CalcMini",
    "Why",
    "Install",
    "Use",
  ];
  // The markup between <Base ...> and </Base>, with comments and the script dropped.
  const body = () => {
    const open = index.indexOf("<Base");
    const start = index.indexOf(">", index.indexOf('path="/"', open)) + 1;
    return index
      .slice(start, index.indexOf("</Base>"))
      .replace(/<script>[\s\S]*?<\/script>/, "")
      .replace(/\{\/\*[\s\S]*?\*\/\}/g, "");
  };

  it("renders each section exactly once, in order", () => {
    const tags = [...body().matchAll(/<([A-Z][A-Za-z]*) \/>/g)].map((m) => m[1]);
    expect(tags).toEqual(TAGS);
  });

  /* position: sticky holds only while its parent is on screen, so the bar must be
   * a direct child of <main> (Base.astro's slot): nothing wraps it here. */
  it("puts the journey bar directly in <main>, right after the hero, wrapped in nothing", () => {
    const markup = body().trim();
    expect(markup.startsWith("<Hero />\n  <JourneyBar />")).toBe(true);
    // Everything between <Base> and its end is a self-closing component tag: no element wraps any of them.
    expect(markup.replace(/<[A-Z][A-Za-z]*\s*\/>/g, "").trim()).toBe("");
  });

  it("renders nothing else: no old section is left", () => {
    expect(index.match(/<[A-Z][A-Za-z]*[ />]/g)?.filter((t) => !/^<(Base|Hero|JourneyBar|HowItWorks|MixMatrixPlay|Audiences|FeatureTour|StreamDeck|CalcMini|Why|Install|Use)\b/.test(t))).toEqual([]);
    expect(index).not.toMatch(/<section/);
  });

  it("carries the page description verbatim", () => {
    expect(index).toContain(
      'description="Multistream to Twitch, YouTube, Kick, Facebook and X from one upload, with a different audio mix per destination and the video untouched. Free, open source, on your own server."',
    );
  });

  it("starts the sections' scripts on an idle callback", () => {
    expect(index).toContain("requestIdleCallback(go");
    expect(index).toContain('import("../scripts/home/boot")');
  });

  it("makes the hero's promise and gives it one primary action", () => {
    const hero = homeFile("Hero.astro");
    // A <br /> between the sentences keeps the accent word on its own line.
    expect(hero).toMatch(/One stream in\.\s*(<br\s*\/?>\s*)?<em class="it it-hot">Different<\/em> audio out\./);
    expect(hero).toMatch(/<a class="btn btn-primary" href="\/download">Install polyemesis<\/a>/);
    expect(hero).toMatch(/<a class="btn btn-ghost" href="#how">/);
  });

  it("uses none of the banned phrases and names no other product", () => {
    // "Stream Deck" then a non-breaking space then "+" is the correct form: only the
    // name written with no space before the plus is banned.
    const banned = /bypass|dodge|DMCA|one maintainer|pre-release|Stream Deck\+|\b\d+\s+stars?\b|music on Twitch and off YouTube/i;
    for (const [name, src] of homeFiles()) {
      expect(src, name).not.toMatch(banned);
      expect(src, name).not.toMatch(COMPETITORS);
    }
  });

  it("has no entrance animation hooks", () => {
    for (const [name, src] of homeFiles()) {
      expect(src, name).not.toContain('class="reveal"');
      expect(src, name).not.toContain("data-reveal");
    }
  });
});

/* Homepage claims checked against the code and docs (T12). Each guard keeps a
 * claim that the docs do not support from coming back, on any homepage file. */
describe("homepage claims match the code (T12)", () => {
  const how = homeFile("HowItWorks.astro");
  const everyFile = () => homeFiles();

  // docs/OBS.md:130-132: classic RTMP carries one audio track. Only SRT/SRTLA carry every track.
  it("does not promise every track over RTMP", () => expect(how).not.toMatch(/RTMP, SRT or SRTLA/));
  // README.md:104 and docs/API.md:893 name vMix only for stop-on-ingest-loss; no multitrack doc.
  it("names no vMix multitrack path in the steps", () => expect(how).not.toMatch(/vMix/));
  // docs/PLATFORMS.md:42-50: many platforms are unverified.
  it("does not promise every platform in How it works", () => expect(how).not.toMatch(/to every platform/));
  it.each([
    // docs/ARCHITECTURE.md:234-236: the outage before the slate stays a timestamp gap.
    ["a gapless failover", /never see a gap/],
    // docs/CONNECTING-PLATFORMS.md:294: reply-to-a-message is unknown on Twitch and YouTube
    // (the send box posts to YouTube, Twitch and Kick).
    ["chat replies", /\breply\b/i],
    // docs/PLATFORMS.md:42-48: chat on several platforms is unverified.
    ["chat for every platform", /chat (for|from) every platform/i],
    // docs/QUICKSTART.md:1-7 is "the impatient path" from installing; it covers no VPS renting.
    ["that the quickstart covers renting a VPS", /renting/],
    // docs/STORAGE.md:3-10, docs/MQTT.md, docs/HOOKS.md send data out when configured.
    ["that all data stays on the server", /keys and data stay/],
  ])("claims no %s", (_what, re) => {
    for (const [name, src] of everyFile()) expect(src, name).not.toMatch(re);
  });
  // docs/FAQ.md:103-104: failover is off by default, so every failover promise is conditional.
  it("makes the failover promise conditional on failover being on", () =>
    expect(homeFile("Why.astro")).toMatch(/With failover on,/));
  // docs/INSTALL.md:327: script installs find the setup code in the closing summary.
  it("points script users at the setup code", () => expect(homeFile("Install.astro")).toMatch(/closing summary/));
  // scripts/install.sh print_summary prints "Open <scheme>://<domain or localhost>:<port>" and the setup code
  // after the script only; `docker run` and Compose print no address (the console is at localhost:8080, docs/QUICKSTART.md),
  // only the setup code in the log. The old guard ("prints the address") kept that claim off the docker steps; this keeps
  // it true everywhere: no tab may say an address is printed, and the script's summary must say what it really gives.
  it("claims no printed console address, and says what the installer's summary gives", () => {
    const install = homeFile("Install.astro");
    expect(install).not.toMatch(/prints the address|address it prints/i);
    expect(install).toMatch(/closing summary gives the address to open \(localhost, unless you named a domain\) and the one-time setup code/);
  });
  // docs/CAPTIONS.md:20 "Every destination starts with captions off"; :14-18 Kick has no caption route.
  it("does not promise every platform a caption route", () =>
    expect(homeFile("FeatureTour.astro")).not.toMatch(/Each destination gets the caption route/));
  // docs/CONNECTING-PLATFORMS.md:290-306: Rumble is read-only and Facebook send is unknown, so send and moderate are conditional.
  it("makes sending and moderating in the chat conditional on the platform", () =>
    expect(homeFile("FeatureTour.astro")).toMatch(/Send and moderate from the same place wherever the platform allows it/));
});
