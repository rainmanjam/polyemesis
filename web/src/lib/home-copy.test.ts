import { readFileSync, readdirSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

import { describe, expect, it } from "vitest";

import { MUSIC_TRACK, TRACK_LAYOUT } from "../data/tracks.mjs";

/* Source-level guards for copy decisions that a rendered page cannot be
 * trusted to keep. They live in src/lib, not beside the page: Astro treats
 * every file under src/pages as a route (see features-shots.test.ts). */

const here = dirname(fileURLToPath(import.meta.url));
const pages = (name: string) => readFileSync(join(here, "..", "pages", name), "utf8");

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

/* THE HERO (2026-11 redesign). One headline with one italic word, a lede,
 * Install as the on-air primary, and the split diagram with its story tabs. */
describe("the home hero (2026-11 redesign)", () => {
  const hero = readFileSync(join(here, "..", "components", "home", "Hero.astro"), "utf8");
  const markup = hero.slice(0, hero.indexOf("<style>"));
  it("states the decided headline with one italic word", () =>
    expect(hero).toMatch(/<h1[^>]*>\s*One video\. A <em class="it">different<\/em> sound for every audience\.\s*<\/h1>/));
  it("states the decided lede", () => {
    expect(hero).toContain("Upload once from OBS, an IRL backpack or any SRT encoder.");
    expect(hero).toContain("with the music on Twitch and off YouTube. Free, and it runs on your own server.");
    expect(markup).not.toMatch(/vMix/);
  });
  it("keeps a space between the lede's two sentences (Astro drops a span's leading whitespace)", () =>
    expect(markup).toContain('any SRT encoder.{" "}<span class="lede-more">polyemesis sends'));
  it("makes Install the on-air primary and How it works the ghost", () => {
    expect(hero).toMatch(/<a[^>]*class="btn btn-onair[^"]*"[^>]*href="#run"[^>]*>\s*Install polyemesis/);
    expect(hero).toMatch(/<a[^>]*class="btn btn-ghost[^"]*"[^>]*href="#how"[^>]*>\s*See how it works/);
  });
  it("renders a diagram panel per story, only Streamer visible", () => {
    expect(hero.match(/<SplitDiagram\b/g)).toHaveLength(1); // inside STORY_ORDER.map
    expect(hero).toMatch(/STORY_ORDER\.map/);
    expect(hero).toMatch(/role="tabpanel"[^>]*hidden=\{k !== "streamer"\}/);
    expect(hero).toMatch(/aria-labelledby=\{`tab-\$\{k\}`\}/);
  });
  it("hides the controls that need JS until JS un-hides them", () => {
    expect(hero).toMatch(/role="tablist"[^>]*hidden/);
    expect(hero).toMatch(/data-pause[^>]*hidden|hidden[^>]*data-pause/);
  });
  it("has a polite live status line and a fine print line rendered from the stories", () => {
    expect(hero).toMatch(/data-status[^>]*aria-live="polite"/);
    expect(hero).toMatch(/data-fine>\{STORIES\[first\]\.fine\}/);
    expect(hero).toMatch(/statusFor\(STORIES\[first\], null, false\)/);
  });
  it("reserves the layout so nothing shifts when script runs (F9)", () => {
    expect(hero).toMatch(/\.tabrow \{ min-height: 44px/);
    expect(hero).toMatch(/\.pause \{ position: absolute/);
    expect(hero).toMatch(/\.status \{[^}]*min-height: 3lh/);
  });
  it("fades the outgoing panel only while motion is on", () => {
    expect(hero).toMatch(/prefers-reduced-motion: no-preference\) \{[^@]*:global\(\.motion-on\) \.panel\.swap-out \{ opacity: 0/);
    expect(hero).not.toMatch(/prefers-reduced-motion: reduce/);
  });
  it("puts no pill above the headline and no interaction colour in it", () => {
    expect(hero.slice(0, hero.indexOf("<h1"))).not.toMatch(/class="chip/);
    expect(hero.slice(hero.indexOf("<h1"), hero.indexOf("</h1>"))).not.toMatch(/\bprimary\b|<span\b/);
  });
  it("loads the behaviour lazily from the page", () => {
    expect(pages("index.astro")).toMatch(/requestIdleCallback\(go/);
    expect(pages("index.astro")).toMatch(/import\("\.\.\/scripts\/home\/boot"\)/);
  });
  it("keeps the italic serif off the preload list and swaps it", () => {
    const base = readFileSync(join(here, "..", "layouts", "Base.astro"), "utf8");
    expect(base).not.toMatch(/rel="preload"[^>]*ibm-plex-serif-italic/);
    expect(base).toMatch(/font-family: "IBM Plex Serif";[^}]*font-style: italic;[^}]*ibm-plex-serif-italic\.woff2|font-family: "IBM Plex Serif";[^}]*ibm-plex-serif-italic\.woff2[^}]*font-style: italic;[^}]*font-display: swap/);
  });
  it("declares the bone ink on the .home wrapper only and the accent font without system serifs", () => {
    const css = readFileSync(join(here, "..", "styles", "global.css"), "utf8");
    expect(css).toMatch(/\.home \{\s*--home-bone: oklch\(93% 0\.014 85\);\s*color: var\(--home-bone\);/);
    expect(css).not.toMatch(/\.home h1|\.home p/);
    expect(css).toContain('--font-accent: "IBM Plex Serif", serif;');
    expect(pages("index.astro")).toMatch(/<div class="home">/);
  });
  it("refreshes the description and keeps the title", () => {
    expect(pages("index.astro")).toContain('title="Multistream, a different mix per platform — polyemesis"');
    expect(pages("index.astro")).toContain(
      'description="Multistream to Twitch, YouTube and Kick from one upload, with a different audio mix per platform and the video untouched. Free, on your own server."',
    );
  });
  it("ships the italic serif under 45 KB with its licence recorded at the top", () => {
    const font = join(here, "..", "..", "public", "fonts", "ibm-plex-serif-italic.woff2");
    expect(statSync(font).size).toBeLessThanOrEqual(45_000);
    const lic = readFileSync(join(here, "..", "..", "public", "third-party-licenses.txt"), "utf8");
    expect(lic).toContain("IBM Plex Serif");
    expect(lic).toContain("SIL OPEN FONT LICENSE Version 1.1");
    expect(lic.indexOf("IBM Plex Serif")).toBeLessThan(lic.indexOf("Data shipped inside the polyemesis server binary"));
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
    expect(source).toMatch(/>Read the source<\/a/);
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

describe("the who-it-is-for link sentences keep their spaces", () => {
  const home = pages("index.astro");
  it.each(["Hosted services are good at that.", "(experimental; Twitch requires a GPU):"])(
    "%s is followed by a space",
    (lead) => {
      const at = home.indexOf(lead);
      expect(at, lead).toBeGreaterThan(-1);
      expect(home.slice(at + lead.length, at + lead.length + 5)).toBe('{" "}');
    },
  );
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

describe("the home page order (2026-11 redesign)", () => {
  const home = pages("index.astro");

  it("runs the five beats, then who it is for, then explore", () => {
    const order = ["<Hero", "<Problem", "<HowItWorks", "<Recovery", "<Run", "Is this for you?", 'aria-label="Explore"'].map((n) => home.indexOf(n));
    expect(order.every((i) => i > -1)).toBe(true);
    expect([...order].sort((a, b) => a - b)).toEqual(order);
  });

  it("phrases who-it-is-for as a choice, in sentence case", () => {
    expect(home).toMatch(/<h3[^>]*>Choose polyemesis if<\/h3>/);
    expect(home).toMatch(/<h3[^>]*>Choose a hosted service if<\/h3>/);
    expect(home).not.toMatch(/>\s*Not built for\s*</);
  });

  it("links the Twitch VOD track page and the trade-offs page", () => {
    expect(home).toMatch(/href="\/twitch-vod-track"/);
    expect(home).toMatch(/href="\/free-restream-service"/);
  });

  it("counts the documents rather than typing a number", () => {
    expect(home).toMatch(/All \{PUBLISHED\.length\} documents/);
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

/* THE ANNOUNCEMENT BAR IS HOME-ONLY (2026-11 design review). Base renders it
 * only for a page that passes `announce`; on docs and task pages it stood
 * between the reader and the task. */
describe("the announcement bar", () => {
  const src = join(here, "..");
  const astroFiles = (dir: string): string[] =>
    readdirSync(join(src, dir), { withFileTypes: true }).flatMap((e) =>
      e.isDirectory() ? astroFiles(join(dir, e.name)) : e.name.endsWith(".astro") ? [join(dir, e.name)] : [],
    );

  // Pages, layouts AND components: a layout such as DocPage passing
  // `announce` to Base would put the bar on every document.
  const everyAstro = () => ["pages", "layouts", "components"].flatMap((d) => astroFiles(d));

  it("is requested by the home page and by nothing else", () => {
    const requesting = everyAstro().filter((f) => /\bannounce=\{/.test(readFileSync(join(src, f), "utf8")));
    expect(requesting).toEqual([join("pages", "index.astro")]);
  });

  it("is rendered only through Base", () => {
    const rendering = everyAstro().filter((f) => /<Announce\b/.test(readFileSync(join(src, f), "utf8")));
    expect(rendering).toEqual([join("layouts", "Base.astro")]);
  });

  it("is rendered by Base only when a page asks for it", () => {
    const base = readFileSync(join(src, "layouts", "Base.astro"), "utf8");
    expect(base).toMatch(/\{announce && <Announce release=\{announce\.release\} \/>\}/);
  });

  it("prints a release only from the build-time read, with an install link otherwise", () => {
    const bar = readFileSync(join(src, "components", "Announce.astro"), "utf8");
    expect(bar).not.toMatch(/\bv\d+\.\d+/);
    expect(bar).toMatch(/href="\/download"/);
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

/* THE SPLIT DIAGRAM (homepage redesign, Task 3). It renders four times on the
 * home page (three hero panels and how-it-works), so any id would repeat, and
 * its destination cards stay generic divs until the hero script upgrades them
 * to buttons: a label on a div is announced inconsistently, so the name lives
 * in data-label until then. */
describe("the split diagram", () => {
  const diagram = () => readFileSync(join(here, "..", "components", "home", "SplitDiagram.astro"), "utf8");

  it("SplitDiagram emits no ids and no aria-label on generic divs", () => {
    const src = diagram();
    expect(src).not.toMatch(/\sid=/);
    expect(src).not.toMatch(/<div[^>]*aria-label/);
  });

  /* geometry.ts's anchors are these tracks' centres: h columns 9/50/91%, v rows
   * .10/.40 and the destination band .56-1. Change one, change the other. */
  it("keeps the grid tracks geometry.ts's anchors are computed from", () => {
    const css = diagram().replace(/\s+/g, " ");
    // v (default): one column, row bands 20/12/16/8/44.
    expect(css).toContain(
      "grid-template-rows: minmax(0, 20fr) minmax(0, 12fr) minmax(0, 16fr) minmax(0, 8fr) minmax(0, 44fr);",
    );
    expect(css).toContain("grid-template-columns: repeat(5, minmax(0, 1fr));"); // sources across
    expect(css).toContain(
      "grid-template-columns: repeat(2, minmax(0, 1fr)); grid-template-rows: repeat(2, minmax(0, 1fr));",
    ); // destinations 2 x 2
    // h (>= 768px): columns 18/22/20/22/18, five source rows, four destination rows.
    expect(css).toContain(
      "grid-template-columns: minmax(0, 18fr) minmax(0, 22fr) minmax(0, 20fr) minmax(0, 22fr) minmax(0, 18fr);",
    );
    expect(css).toContain("grid-template-rows: repeat(5, minmax(0, 1fr));");
    expect(css).toContain("grid-template-rows: repeat(4, minmax(0, 1fr));");
  });
});

describe("the run beat", () => {
  const run = readFileSync(join(here, "..", "components", "home", "Run.astro"), "utf8");
  it("uses the real install commands", () => {
    expect(run).toContain("rainmanjam/polyemesis:latest");
    expect(run).toContain("-p 8080:8080 -p 6000:6000/udp -p 1935:1935");
    const install = readFileSync(join(here, "..", "..", "..", "docs", "INSTALL.md"), "utf8");
    const script = install.slice(install.indexOf("curl --proto"), install.indexOf("sudo bash install.sh") + "sudo bash install.sh".length);
    // Every line of the script block, not just its URL: Run.astro's template
    // literal escapes the line-continuation backslash, so unescape it first.
    const shipped = run.match(/const script = `([\s\S]*?)`;/)?.[1].replace(/\\\\/g, "\\");
    expect(shipped).toBe(script);
  });
  it("names no placeholder install", () => expect(run).not.toMatch(/polyemesis\.com\/install|polyemesis\/polyemesis\b|href="#"/));
  it("does not claim the console address is printed", () => expect(run).not.toMatch(/prints the address/));
});

/* Task 12: homepage claims checked against the code and docs. Each guard keeps
 * a claim that the docs do not support from coming back. */
describe("homepage claims match the code (T12)", () => {
  const home = (name: string) => readFileSync(join(here, "..", "components", "home", name), "utf8");
  const how = home("HowItWorks.astro");
  const recovery = home("Recovery.astro");
  const run = home("Run.astro");

  // docs/OBS.md:130-132: classic RTMP carries one audio track. Only SRT/SRTLA carry every track.
  it("does not promise every track over RTMP", () => expect(how).not.toMatch(/RTMP, SRT or SRTLA/));
  // README.md:104 and docs/API.md:893 name vMix only for stop-on-ingest-loss; no multitrack doc.
  it("names no vMix multitrack path in the steps", () => expect(how).not.toMatch(/vMix/));
  // docs/ARCHITECTURE.md:234-236: the outage before the slate stays a timestamp gap.
  it("does not promise a gapless failover", () => expect(recovery).not.toMatch(/never see a gap/));
  // docs/CONNECTING-PLATFORMS.md:294: reply-to-a-message is unknown on Twitch and YouTube.
  it("does not claim chat replies", () => expect(recovery).not.toMatch(/\breply\b/i));
  // docs/PLATFORMS.md:42-48: chat on several platforms is unverified.
  it("does not claim chat for every platform", () => expect(recovery).not.toMatch(/chat (for|from) every platform/i));
  // docs/QUICKSTART.md:1-7 is "the impatient path" from installing; it covers no VPS renting
  // and sends readers to INSTALL.md for "the careful version".
  it("does not claim the quickstart covers renting a VPS", () => expect(run).not.toMatch(/renting/));
  it("sends server newcomers to the install guide", () => expect(run).toMatch(/Never run a server before\?[^<]*<a href="\/docs\/install">/));
  // docs/INSTALL.md:327: script installs find the setup code in the closing summary.
  it("points script users at the setup code", () => expect(run).toMatch(/closing summary/));
  // docs/FAQ.md:103-104: failover is off by default, so the heading must be conditional.
  it("makes the recovery heading conditional on failover", () =>
    expect(recovery).toMatch(/With failover on, your stream stays up when your encoder drops\./));
  // docs/STORAGE.md:3-10, docs/MQTT.md, docs/HOOKS.md send data out when configured.
  it("does not claim all data stays on the server", () => expect(run).not.toMatch(/keys and data stay/));
  // docs/PLATFORMS.md:42-50: many platforms are unverified.
  it("does not promise every platform in How it works", () => expect(how).not.toMatch(/to every platform/));
  // Problem: competitor claims scoped to what docs/COMPARISON.md and the facts file show.
  const problem = home("Problem.astro");
  it("makes no universal competitor claims", () => {
    expect(problem).not.toMatch(/Every way to multistream/);
    expect(problem).not.toMatch(/usually means a paid plan/);
    expect(problem).not.toMatch(/any mix you can script/);
  });
  // Spec "Decided": competitors stay generic (not named). Sources live in the PR claims table.
  it("names no competitor in the problem cards", () =>
    expect(problem).not.toMatch(/Restream|Streamlabs|StreamElements|Castr|Aitum|obs-multi-rtmp|MistServer/i));
  // upload-meter.test.ts builds its fixture by hand; these pin the hooks it
  // relies on to the markup that ships.
  it("renders the meter hooks upload-meter.ts reads", () => {
    expect(problem).toMatch(/<span data-pcount>/);
    expect(problem.match(/<i data-kind=\{/g)).toHaveLength(2); // the cards' map and the strip
    expect(problem.match(/<span class="val" data-val=\{/g)).toHaveLength(2);
  });
  it("lets screen readers read the platform count the meters count against", () =>
    expect(problem).toMatch(/<p class="pc">Platforms you stream to:/));
});
