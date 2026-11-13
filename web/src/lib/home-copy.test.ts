import { readFileSync, readdirSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

import { describe, expect, it } from "vitest";

import { COMPETITORS } from "./competitors";
import { MUSIC_TRACK, TRACK_LAYOUT } from "../data/tracks.mjs";

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

describe("the features page's track layout (S7)", () => {
  const features = pages("features.astro");

  /* The Proof capture numbers its tracks differently from the OBS layout in
   * the problem section. The fix is one layout per place it is used, with the
   * capture's own legend stated once in a single line rather than a paragraph
   * of disclaimer. */
  it("states the capture's track layout in one legend line", () => {
    const legends = features.match(/Track layout in this capture/g) ?? [];
    expect(legends.length).toBe(1);
  });

  /* ONE LAYOUT, NOT ONE PER SECTION. The legend above used to exist because
   * the problem section, the patchbay and the capture each numbered the tracks
   * differently. Now all three read src/data/tracks.mjs, and that file restates
   * the seeded install the screenshots are taken from. These pin both halves:
   * nothing on the page keeps its own copy, and the copy they share matches
   * what the capture actually photographed. */
  it("reads the track layout from the shared module everywhere it is used", () => {
    expect(features).toMatch(/from "\.\.\/data\/tracks\.mjs"/);
    const matrix = readFileSync(join(here, "..", "components", "MixMatrix.astro"), "utf8");
    expect(matrix).toMatch(/const TRACKS = TRACK_LAYOUT;/);
    // The six-track layout this replaced, so it cannot come back by paste.
    expect(matrix).not.toMatch(/"Full mix"|"Clean mix"/);
    expect(features).not.toMatch(/Track 1 is the full mix/i);
  });

  it("matches the layout of the seeded install the screenshots come from", () => {
    const fixture = JSON.parse(
      readFileSync(join(here, "..", "..", "..", "scripts", "demo-seed.fixture.json"), "utf8"),
    ) as { sources: { audio: { label: string }[] }[] };
    const captured = fixture.sources[0].audio.map((a) => a.label);
    expect(
      TRACK_LAYOUT.map((t) => t.fixtureLabel),
      "src/data/tracks.mjs no longer matches the first programme in " +
        "scripts/demo-seed.fixture.json, so the features page names tracks the " +
        "routing and meters screenshots do not show",
    ).toEqual(captured);
    expect(TRACK_LAYOUT.find((t) => t.id === MUSIC_TRACK)?.fixtureLabel).toMatch(/music/i);
  });

  it("does not end the payoff paragraph with the Twitch GPU requirement", () => {
    const payoff = features.slice(features.indexOf("polyemesis ingests once and lets you pick"));
    const paragraph = payoff.slice(0, payoff.indexOf("</p>")).replace(/<[^>]+>/g, "");
    expect(paragraph).not.toMatch(/GPU/);
    expect(paragraph).not.toMatch(/Enhanced Broadcasting/);
  });

  it("links the Twitch GPU detail to the Twitch route page", () => {
    expect(features).toMatch(/href="\/twitch-vod-track"/);
  });
});

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

/* THE PLAN STRIP (S3). Every figure here must trace to the dated evidence
 * file, and scripts/check-build.mjs requires a page that cites that file to
 * show a check date the file itself claims. The two Restream qualifiers the
 * file records as UNSUPPORTED ("personal profiles only", "excludes Facebook
 * Pages") must not be published, and the word "watermark" is the page's own
 * word for Restream's branding, not the evidence file's. */
describe("the features plan strip (S3)", () => {
  const source = pages("features.astro");
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
});

/* THE DASHBOARD'S INGEST UPTIME. #924 took "uptime" out of the features copy
 * because it read "—" on the one-port SRT/RTMP ingest, which has no ingest
 * process to time. The programme's relay hub now times the stream from when it
 * started arriving (relay.Stats.ReceivingSec), so the card shows it on every
 * ingest mode and the copy says so again. */
describe("the features page's dashboard copy", () => {
  it("claims the ingest uptime the card now shows on every ingest mode", () => {
    const features = pages("features.astro");
    const dashboard = features.slice(features.indexOf("The programme you picked in the header"));
    const sentence = dashboard.slice(0, dashboard.indexOf("\n"));
    expect(sentence).toMatch(/its ingest \(bitrate, uptime,/);
  });
});

/* THE FEATURES PAGE'S ORDER (2026-11 design review, moved from the home page
 * in the redesign). A visitor should meet the problem, try the patchbay and
 * see the proof straight after the spec strip, before the screens, the rooms
 * and the hosted-pricing comparison, so the price table answers "what does
 * this cost" for someone who already wants it. */
describe("the features page order", () => {
  const features = pages("features.astro");
  const at = (needle: string) => {
    const i = features.indexOf(needle);
    expect(i, needle).toBeGreaterThan(-1);
    return i;
  };

  it("puts the problem and the patchbay straight after the spec strip, before the price table", () => {
    const spec = at("<SpecStrip />");
    const problem = at("You send four tracks. Every platform will take one stereo pair.");
    const patchbay = at("<MixMatrix />");
    const prices = at("<caption class=\"sr-only\">polyemesis compared with Restream's hosted plans</caption>");
    expect(spec).toBeLessThan(problem);
    expect(problem).toBeLessThan(patchbay);
    expect(patchbay).toBeLessThan(prices);
  });

  it("reads problem, patchbay, proof before the screens and the rooms", () => {
    const spec = at("<SpecStrip />");
    const problem = at("You send four tracks. Every platform will take one stereo pair.");
    const proof = at("Loudness measured per destination, after routing, from one ingest.");
    const screens = at("sections.map((s, i) => (");
    const rooms = at('<p class="eyebrow">Who it is for</p>');
    // Nothing but the problem's own comment sits between the strip and the problem.
    expect(features.slice(spec, problem)).not.toMatch(/<section[\s\S]*<section/);
    expect(problem).toBeLessThan(proof);
    expect(proof).toBeLessThan(screens);
    expect(screens).toBeLessThan(rooms);
  });
});

describe("nothing that left the home page left the site", () => {
  const features = pages("features.astro");
  it.each([
    "<SpecStrip />",
    "You send four tracks. Every platform will take one stereo pair.",
    "<MixMatrix />",
    "Loudness measured per destination, after routing, from one ingest.",
    "<LoudnessBars />",
    "Track layout in this capture",
    "The rest of it is a broadcast server.",
    "Decisions you can rely on.",
    "Hosted restreamers charge by destination. This one charges for a server.",
    "Checked 16 September 2026",
  ])("features.astro carries %s", (s) => expect(features).toContain(s));

  it("the home page carries none of them", () => {
    const home = pages("index.astro");
    for (const s of ["<MixMatrix />", "<LoudnessBars />", "<SpecStrip />", "The rest of it is a broadcast server."]) {
      expect(home).not.toContain(s);
    }
  });
});

/* THE FEATURES PAGE'S SCREENSHOTS AND SECTION HEADS (2026-11 design review). */
describe("the features page sections", () => {
  const features = pages("features.astro");

  it("lazy-loads every capture, none being near the fold, with a placeholder under each", () => {
    expect(features).not.toMatch(/loading=\{i === 0|fetchpriority=\{i === 0|loading="eager"/);
    expect(features).toMatch(/style=\{placeholder\.get\(s\.shot\)\}/);
    expect(features).toMatch(/style=\{placeholder\.get\(s\.setup\.shot\)\}/);
  });

  it("does not repeat each section's title in an eyebrow above it", () => {
    expect(features).not.toMatch(/<p class="eyebrow">\{s\.eyebrow\}<\/p>/);
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
