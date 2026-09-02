// @ts-check
/* WHICH docs/*.md files become pages on polyemesis.com, and what each one says
 * to a search engine.
 *
 * THIS IS AN ALLOWLIST AND IT MUST STAY ONE. The obvious implementation is a
 * glob over docs/ with a handful of exclusions, and it is wrong in a way that
 * only shows up later: it makes the NEXT internal note added to docs/ public by
 * default. Publishing an internal document is not a broken page, it is a
 * self-inflicted wound, and nobody would notice until someone else did.
 *
 * The documents that argument was written about are no longer in this
 * repository at all — they live under docs/internal/, which is git-ignored,
 * because "not linked from the website" and "not on the internet" are different
 * things and only the second one was ever the goal. This list is now the second
 * line of defence rather than the only one.
 *
 * So the two tables below partition docs/*.md, and check-build.mjs fails if a
 * file in that directory appears in neither. Adding a document is therefore a
 * decision somebody has to write down rather than a default.
 *
 * TITLES AND DESCRIPTIONS ARE WRITTEN, NOT GENERATED. None of these files has
 * frontmatter; every one starts at `# Title`. A description sliced out of the
 * first paragraph is worth approximately nothing in a search result — it is the
 * one piece of copy a reader sees before deciding whether to click, and it is
 * the only part of this whole change that is not plumbing. Titles are kept under
 * ~60 characters and descriptions under ~155, because Google truncates around
 * there and a sentence cut mid-clause reads as neglect.
 *
 * ===== WHY A TABLE, AND WHAT IS DERIVED FROM IT =====
 *
 * This was 23 object literals of five fields each, and two of the five were
 * restatements of things already known. `slug` was `file` lowercased with the
 * extension dropped, typed out 23 times; `section` was the heading of the
 * comment the entry sat under. Both are derived now, which removes 46 chances
 * to mistype a string that decides a URL — and a wrong slug is not a compile
 * error, it is a page at the wrong address with 110 cross-document links
 * pointing at the old one.
 *
 * The same restructure is what fixed ui/src/lib/tourSteps.ts under the same
 * SonarCloud rule (#388), and for the same reason: a file whose entries are
 * ONLY the decisions somebody actually made has nothing left to repeat. The
 * duplication finding was the symptom. Two derivable fields written out by hand
 * was the defect.
 *
 * GROUPED BY SECTION, WHICH IS ALSO NOT COSMETIC. Filing each document under
 * the section it belongs to is what makes `section` derivable at all, and it
 * puts the sidebar's order and the index page's order in one place instead of
 * two that can disagree.
 */

/** Where an unpublished document, or a file outside docs/, is linked instead. */
export const GITHUB_BLOB = "https://github.com/rainmanjam/polyemesis/blob/main";

/** Where a linked DIRECTORY lives. GitHub serves trees and blobs on different paths. */
export const GITHUB_TREE = "https://github.com/rainmanjam/polyemesis/tree/main";

/**
 * @typedef {object} Doc
 * @property {string} file      Filename under docs/, e.g. "AUDIO-ROUTING.md".
 * @property {string} slug      URL segment under /docs/, e.g. "audio-routing".
 * @property {string} title     `<title>` and og:title. Under ~60 chars.
 * @property {string} description `<meta name=description>`, and the blurb on the
 *   docs index. One string rather than two, because two would drift and the
 *   index blurb and the search snippet are answering the same question.
 * @property {string} section   Which group on the index and in the sidebar.
 * @property {string} [canonical] Canonical path, when it is NOT this page's own
 *   URL. Only /docs/comparison has one; see CANONICAL_TO below.
 */

/** The URL segment a document is served at, derived from its filename.
 *
 *  Every published document is SHOUTING-KEBAB.md and every URL is the same name
 *  in lower case, so this is the whole rule. It is a function rather than 23
 *  literals because the literals were the only thing that could ever disagree
 *  with it. check-build.mjs asserts the result is URL-safe, which is what stops
 *  a future `NOTES_v2.md` from quietly becoming `/docs/notes_v2`.
 *
 *  @param {string} file @returns {string} */
export const slugOf = (file) => file.replace(/\.md$/, "").toLowerCase();

/* CANONICALISED TO /comparison, AND KEPT OUT OF THE SITEMAP.
 *
 * The site already has a comparison page, and it and this document answer one
 * query — "restreamer alternative", "obs multi rtmp". Two indexed pages on one
 * intent do not double the chances; they split the signal and let a search
 * engine pick, which is the outcome neither page was written for.
 *
 * Publishing anyway, rather than dropping it, because the two are not the same
 * page doing the same job. /comparison is the ARGUMENT, written to be read by
 * someone deciding. This document is the EVIDENCE behind it: dated sources,
 * competitor tracker issue numbers, and the footnote recording that a row said
 * "Yes" until 2026-08-09 and was wrong. Five other documents link to it; sending
 * those links back out to github.com is the exact problem rendering the docs
 * here exists to fix.
 *
 * So: reachable, linked, indexed under /comparison's authority — and absent from
 * the sitemap, because submitting a URL that declares a different canonical is a
 * contradiction a crawler resolves by trusting neither.
 *
 * A map with one entry rather than a sixth column that 22 rows leave empty. The
 * exception is stated once, where its reasoning fits, instead of being a mostly
 * absent field nobody can see the shape of.
 * @type {Map<string, string>} */
export const CANONICAL_TO = new Map([["COMPARISON.md", "/comparison"]]);

/** One row of a section's table: the file, and the two strings written for it.
 *
 *  Positional, and the columns cannot be swapped by accident: a file ends in
 *  `.md`, a title is short enough to survive a SERP and a description is not.
 *  @typedef {[file: `${string}.md`, title: string, description: string]} DocRow */

/** The sections, in the order they appear in the sidebar and on the index page.
 *  @type {[id: SectionId, title: string, blurb: string][]} */
const SECTION_ROWS = [
  ["start", "Getting it running", "Install it, point an encoder at it, and put a certificate in front of it."],
  ["routing", "The routing", "The part the rest of the product exists to serve: a different audio mix per destination."],
  ["operating", "Operating it", "Configuration, platforms, upgrades, and what to do when it will not connect."],
  ["automation", "Automating it", "Metrics, webhooks, MQTT and the HTTP API — everything that lets something else drive."],
  ["reference", "Understanding it", "How it fits together, what it depends on, and how it compares."],
];

/** @typedef {"start" | "routing" | "operating" | "automation" | "reference"} SectionId */

/* WHICH DOCUMENTS ARE IN WHICH SECTION, keyed by the section id.
 *
 * A KEYED OBJECT AND NOT A FOURTH COLUMN ON THE ROWS ABOVE, for two reasons
 * that happen to point the same way.
 *
 * The plain one: a key cannot be duplicated or misspelled without something
 * noticing. `SectionId` makes a typo a type error, and there is no way to write
 * two `start` groups the way two rows could both claim `section: "start"`.
 *
 * The other one is about the SonarCloud duplication gate this shape was chosen
 * to satisfy, and it is worth writing down because it is not obvious and the
 * next person to restructure this file will undo it by accident. Sonar's
 * copy/paste detector normalises every string literal to one generic token --
 * which is why 23 different titles never broke the match, and why the previous
 * arrangement (a section header of three strings, then rows of three strings)
 * was one long uniform token stream that matched itself across group
 * boundaries at 126 tokens. An IDENTIFIER is not normalised. So `start`,
 * `routing`, `operating`, `automation` and `reference` below are five tokens
 * that differ from each other and from everything else, and a duplicate run can
 * no longer run through a boundary: the longest one left is the interior of the
 * biggest section, six rows, well under the threshold.
 *
 * Measured, not assumed — see the note in the pull request.
 * @type {Record<SectionId, DocRow[]>} */
const DOCS_BY_SECTION = {
  start: [
    ["QUICKSTART.md", "Quickstart: from nothing to a live restream",
      "Five minutes: run it, set a password, point OBS at the SRT ingest, name the tracks, add one destination, and check the fan-out is actually right."],
    /* "SRT server" in the title, and it is not a keyword bolted on: an SRT
     * listener that ingests and fans out is what this is, and it is the phrase
     * people type. The project's own documents never once call it that — 49
     * mentions of SRT in this file and not one "SRT server" — which is how a
     * page ends up invisible for the thing it does. */
    ["INSTALL.md", "Install polyemesis — an SRT server on your own box",
      "One binary and FFmpeg 6.0 or newer, built with SRT. Docker, the systemd unit, the macOS and Windows paths, and how to verify the install really works."],
    /* The detail in the description is the one that costs people an evening:
     * multitrack SRT out of OBS is configured on the RECORDING tab as a Custom
     * Output (FFmpeg), not on the Stream tab where everyone looks first. It is
     * also, conveniently, where "OBS SRT" belongs in a sentence. */
    ["OBS.md", "OBS SRT setup: multitrack audio to one ingest",
      "Sending OBS multitrack audio over SRT is a Custom Output on the Recording tab, not the Stream tab. Encoder settings that work, plus RTMP fallback."],
    ["TLS.md", "TLS certificates for a self-hosted SRT server",
      "Let's Encrypt when the box has a public name, a built-in CA when it does not, and what changes behind a reverse proxy. Plus why HSTS is opt-in here."],
  ],

  routing: [
    /* THE TWITCH VOD TRACK IS DOCUMENTED HERE, not on PLATFORMS.md — ten
     * mentions in this file against none in that one. So the phrase goes in
     * this description, where it describes the page.
     *
     * Scoped to Twitch on purpose, and the project's copy rules are why: a
     * second audio track is a Twitch Enhanced Broadcasting capability, and
     * nothing here has measured YouTube, Kick, Facebook or Rumble accepting one.
     * The same rules exclude the tempting version of this sentence — describe the
     * mechanism, do not promise an outcome nobody here controls. */
    ["AUDIO-ROUTING.md", "Audio routing: a different mix per destination",
      // 172 characters before this edit, against the ~155 Google truncates at.
      // The clause that went is the Twitch VOD track, which was doing least work
      // in a description about the mix matrix and now has /twitch-vod-track to
      // itself.
      "Up to 32 tracks in, one FFmpeg filter graph per destination out. The mix matrix, per-cell gain, and how the two-mix egress is built."],
    ["RENDITIONS.md", "Renditions: one shared video encode",
      "A named video profile several destinations share, for platforms that refuse your source. What it re-encodes, when it runs, and what passthrough costs."],
    ["ENCODING.md", "Encoding: what is copied and what is encoded",
      "Video is stream-copied by default; audio is encoded once per destination mix. Where a GPU actually helps, rate control, frame rate, and the cost."],
    ["HARDWARE.md", "Hardware encoding: NVENC, QSV, VA-API, AMF",
      "GPU encoding for renditions, what each vendor needs, running it under Docker, and an explicit account of what this project has and has not observed."],
  ],

  operating: [
    ["CONFIGURATION.md", "Configuration: config.yaml and the web UI",
      "Two places a setting can live, and they are not interchangeable. Every config.yaml key, its default, the data directory, and the environment variables."],
    ["PLATFORMS.md", "Streaming platforms: what can be automated",
      "YouTube, Twitch, Kick, Facebook and Rumble. Anything taking RTMP or SRT works with a pasted key; this is what each platform's API lets you automate."],
    ["SCHEDULED-BROADCAST.md", "Broadcasting from a file, on a schedule",
      "Go live from a recorded file at a time you choose, with no encoder attached. A file:// pull ingest plus a schedule, and a failover playlist as filler."],
    ["HOT-RELOAD.md", "What a settings change restarts, and what it does not",
      "The rule is mechanical: a value that reaches an FFmpeg command line replaces the process running it. What a restart costs, and what a save now tells you."],
    ["UPGRADING.md", "Upgrading polyemesis and its database",
      "Stop, replace the binary, start — the database migrates itself forward. What to back up first, the version-specific notes, and how to roll back."],
    ["TROUBLESHOOTING.md", "Troubleshooting: SRT, RTMP and audio problems",
      "Organised by what you can observe. SRT handshake failures, an ingest that never goes live, a destination producing nothing, and audio that is wrong."],
  ],

  automation: [
    ["MONITORING.md", "Monitoring: Prometheus metrics and alerts",
      "Everything the dashboard draws, as metrics. The endpoint and why it needs authentication, metric conventions, queries to start from, and built-in alerts."],
    ["HOOKS.md", "Lifecycle webhooks: one signed POST per event",
      "A signed JSON POST when a stream starts or stops and when a destination goes live or drops. The envelope, verifying a signature, ordering and gaps."],
    ["MQTT.md", "MQTT telemetry and Home Assistant",
      "Retained state on an MQTT 5.0 broker, so a consumer that was offline still gets the current answer. The topic tree, the payloads, and Home Assistant."],
    ["API.md", "HTTP API reference — polyemesis /api/v1",
      "Everything the web UI does, it does through this API. Authentication, conventions, every route, the WebSocket, and a worked example end to end."],
  ],

  reference: [
    ["ARCHITECTURE.md", "Architecture: relay hub, engines, FFmpeg children",
      "Ingest once, fan out to N destinations. The process graph, the audio routing engine, the shared rendition encode, and the package layout."],
    ["FAQ.md", "polyemesis FAQ — self-hosted multistreaming",
      "What it is for, how it differs from Restreamer and restream.io, whether your video is re-encoded, how many destinations, and whether SRT is required."],
    ["TESTING.md", "Testing polyemesis without OBS or a camera",
      "FFmpeg only. Generate a multitrack SRT stream of distinct sine tones, route the tracks differently per destination, and measure what actually came out."],
    ["DEPENDENCIES.md", "Dependencies, and why each one is here",
      "Why every Go and frontend dependency is present, why some obvious alternatives are not, and why FFmpeg is a subprocess rather than a linked library."],
    /* Canonicalised to /comparison. The reasoning is on CANONICAL_TO above,
       because it is about the relationship between two pages rather than about
       this row. */
    ["COMPARISON.md", "polyemesis vs Restreamer, restream.io, obs-multi-rtmp",
      "An honest comparison, including where polyemesis loses. Sourced from the competitors' own trackers and plan pages, with every claim dated."],
  ],
};

/** The order here is the order in the sidebar and on the index page.
 * @type {{ id: string, title: string, blurb: string }[]} */
export const SECTIONS = SECTION_ROWS.map(([id, title, blurb]) => ({ id, title, blurb }));

/** Every published document, flattened in sidebar order.
 *
 * Driven by SECTION_ROWS rather than by `Object.keys(DOCS_BY_SECTION)`, so the
 * one explicit ordering in this file is the one that decides. A section listed
 * above with no entry below would silently contribute nothing; check-build.mjs
 * fails on that rather than leaving it to be noticed as a gap in the sidebar.
 * @type {Doc[]} */
export const PUBLISHED = SECTION_ROWS.flatMap(([section]) =>
  (DOCS_BY_SECTION[section] ?? []).map(([file, title, description]) => {
    const canonical = CANONICAL_TO.get(file);
    return { file, slug: slugOf(file), title, description, section, ...(canonical ? { canonical } : {}) };
  }),
);

/** The section ids that have a table, for the guard in check-build.mjs.
 * @type {string[]} */
export const SECTION_IDS_WITH_DOCS = Object.keys(DOCS_BY_SECTION);

/* NOT PUBLISHED, each with the reason — because "it is not in the other list"
 * is not a reason, and the next person deciding where a new file goes needs to
 * see what kind of thing lands here.
 *
 * Everything left here is internal by nature rather than by risk: design notes
 * for work not yet done, an internal counterpart to a published document, and a
 * runbook. Anything whose CONTENT should not be public no longer belongs in this
 * list — it belongs in docs/internal/, which git does not track. A row here
 * still means "in the repository, just not on the website". */

/** @typedef {[file: `${string}.md`, why: string]} WithheldRow */

/** @type {WithheldRow[]} */
const WITHHELD_ROWS = [
  ["SITE-DEPLOY.md", "Deployment runbook for this website."],
  ["TEST-STRATEGY.md", "Internal test strategy; TESTING.md is the user-facing half."],
  ["MODULES.md", "Internal package inventory; ARCHITECTURE.md is the user-facing half."],
  ["DESIGN-SYSTEM.md", "Design tokens for the app UI."],
  ["DESIGN-DESTINATION-HEALTH.md", "Design note for unshipped work."],
  ["DESIGN-ONE-PORT-ONLY.md", "Design note for unshipped work."],
  ["DESIGN-ONE-PORT-INGEST.md", "Design note for unshipped work."],
  ["DESIGN-OAUTH-BROKER.md", "Design note for unshipped work."],
  ["STATUS.md", "Point-in-time readiness audit (2026-09-01); a snapshot of what was true that week, not documentation. Publishing it would put a date-stamped list on the site that nothing updates."],
  ["PATH-TO-PRODUCTION.md", "The remediation plan behind STATUS.md, and stale the moment an item is done. The findings it summarises are public as issues #642-#649 and #651, which is where a reader should be sent."],
  ["README.md", "A directory of the others; /docs is that page here."],
];

/** @type {{ file: string, why: string }[]} */
export const NOT_PUBLISHED = WITHHELD_ROWS.map(([file, why]) => ({ file, why }));

/** Source-file lookup, for link rewriting and the sitemap's lastmod.
 * @type {Map<string, Doc>} */
export const BY_FILE = new Map(PUBLISHED.map((d) => [d.file, d]));

/** Slug lookup, for the page that is being rendered.
 * @type {Map<string, Doc>} */
export const BY_SLUG = new Map(PUBLISHED.map((d) => [d.slug, d]));

/** The site path a published document is served at.
 * @param {Doc} doc
 * @returns {string} */
export const hrefOf = (doc) => `/docs/${doc.slug}`;

/** Documents in a section, in manifest order.
 * @param {string} sectionId
 * @returns {Doc[]} */
export const docsIn = (sectionId) => PUBLISHED.filter((d) => d.section === sectionId);
