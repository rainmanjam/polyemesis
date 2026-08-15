// @ts-check
/* WHICH docs/*.md files become pages on polyemesis.com, and what each one says
 * to a search engine.
 *
 * THIS IS AN ALLOWLIST AND IT MUST STAY ONE. The obvious implementation is a
 * glob over docs/ with a handful of exclusions, and it is wrong in a way that
 * only shows up later: it makes the NEXT internal note added to docs/ public by
 * default. Two of the files in that directory are exactly the kind that must
 * never ship — RESEARCH-COMPETITIVE.md is a survey of competitors' issue
 * trackers, and COPY-CONSTRAINTS.md records what this project's marketing copy
 * may and may not claim. Publishing either is not a broken page, it is a
 * self-inflicted wound, and nobody would notice until someone else did.
 *
 * So the two lists below partition docs/*.md, and check-build.mjs fails if a
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
 *   URL. Only /docs/comparison sets it; see the note on that entry.
 */

/** The order here is the order in the sidebar and on the index page.
 * @type {{ id: string, title: string, blurb: string }[]} */
export const SECTIONS = [
  {
    id: "start",
    title: "Getting it running",
    blurb: "Install it, point an encoder at it, and put a certificate in front of it.",
  },
  {
    id: "routing",
    title: "The routing",
    blurb: "The part the rest of the product exists to serve: a different audio mix per destination.",
  },
  {
    id: "operating",
    title: "Operating it",
    blurb: "Configuration, platforms, upgrades, and what to do when it will not connect.",
  },
  {
    id: "automation",
    title: "Automating it",
    blurb: "Metrics, webhooks, MQTT and the HTTP API — everything that lets something else drive.",
  },
  {
    id: "reference",
    title: "Understanding it",
    blurb: "How it fits together, what it depends on, and how it compares.",
  },
];

/** @type {Doc[]} */
export const PUBLISHED = [
  // ------------------------------------------------------------------ start
  {
    file: "QUICKSTART.md",
    slug: "quickstart",
    title: "Quickstart: from nothing to a live restream",
    description:
      "Five minutes: run it, set a password, point OBS at the SRT ingest, name the tracks, add one destination, and check the fan-out is actually right.",
    section: "start",
  },
  {
    /* "SRT server" in the title, and it is not a keyword bolted on: an SRT
     * listener that ingests and fans out is what this is, and it is the phrase
     * people type. The project's own documents never once call it that — 49
     * mentions of SRT in this file and not one "SRT server" — which is how a
     * page ends up invisible for the thing it does. */
    file: "INSTALL.md",
    slug: "install",
    title: "Install polyemesis — an SRT server on your own box",
    description:
      "One binary and FFmpeg 6.0 or newer, built with SRT. Docker, the systemd unit, the macOS and Windows paths, and how to verify the install really works.",
    section: "start",
  },
  {
    /* The detail in the description is the one that costs people an evening:
     * multitrack SRT out of OBS is configured on the RECORDING tab as a Custom
     * Output (FFmpeg), not on the Stream tab where everyone looks first. It is
     * also, conveniently, where "OBS SRT" belongs in a sentence. */
    file: "OBS.md",
    slug: "obs",
    title: "OBS SRT setup: multitrack audio to one ingest",
    description:
      "Sending OBS multitrack audio over SRT is a Custom Output on the Recording tab, not the Stream tab. Encoder settings that work, plus RTMP fallback.",
    section: "start",
  },
  {
    file: "TLS.md",
    slug: "tls",
    title: "TLS certificates for a self-hosted SRT server",
    description:
      "Let's Encrypt when the box has a public name, a built-in CA when it does not, and what changes behind a reverse proxy. Plus why HSTS is opt-in here.",
    section: "start",
  },

  // ----------------------------------------------------------------- routing
  {
    /* THE TWITCH VOD TRACK IS DOCUMENTED HERE, not on PLATFORMS.md — ten
     * mentions in this file against none in that one. So the phrase goes in
     * this description, where it describes the page.
     *
     * Scoped to Twitch on purpose, and docs/COPY-CONSTRAINTS.md §1 is why: a
     * second audio track is a Twitch Enhanced Broadcasting capability, and
     * nothing here has measured YouTube, Kick, Facebook or Rumble accepting one.
     * §4 rules out the tempting version of this sentence as well — describe the
     * mechanism, do not promise an outcome nobody here controls. */
    file: "AUDIO-ROUTING.md",
    slug: "audio-routing",
    title: "Audio routing: a different mix per destination",
    description:
      "Up to 32 tracks in, one FFmpeg filter graph per destination out. The mix matrix, per-cell gain, and a second Twitch VOD track mix from one ingest.",
    section: "routing",
  },
  {
    file: "RENDITIONS.md",
    slug: "renditions",
    title: "Renditions: one shared video encode",
    description:
      "A named video profile several destinations share, for platforms that refuse your source. What it re-encodes, when it runs, and what passthrough costs.",
    section: "routing",
  },
  {
    file: "ENCODING.md",
    slug: "encoding",
    title: "Encoding: what is copied and what is encoded",
    description:
      "Video is stream-copied by default; audio is encoded once per destination mix. Where a GPU actually helps, rate control, frame rate, and the cost.",
    section: "routing",
  },
  {
    file: "HARDWARE.md",
    slug: "hardware",
    title: "Hardware encoding: NVENC, QSV, VA-API, AMF",
    description:
      "GPU encoding for renditions, what each vendor needs, running it under Docker, and an explicit account of what this project has and has not observed.",
    section: "routing",
  },

  // --------------------------------------------------------------- operating
  {
    file: "CONFIGURATION.md",
    slug: "configuration",
    title: "Configuration: config.yaml and the web UI",
    description:
      "Two places a setting can live, and they are not interchangeable. Every config.yaml key, its default, the data directory, and the environment variables.",
    section: "operating",
  },
  {
    file: "PLATFORMS.md",
    slug: "platforms",
    title: "Streaming platforms: what can be automated",
    description:
      "YouTube, Twitch, Kick, Facebook and Rumble. Anything taking RTMP or SRT works with a pasted key; this is what each platform's API lets you automate.",
    section: "operating",
  },
  {
    file: "SCHEDULED-BROADCAST.md",
    slug: "scheduled-broadcast",
    title: "Broadcasting from a file, on a schedule",
    description:
      "Go live from a recorded file at a time you choose, with no encoder attached. A file:// pull ingest plus a schedule, and a failover playlist as filler.",
    section: "operating",
  },
  {
    file: "HOT-RELOAD.md",
    slug: "hot-reload",
    title: "What a settings change restarts, and what it does not",
    description:
      "The rule is mechanical: a value that reaches an FFmpeg command line replaces the process running it. What a restart costs, and what a save now tells you.",
    section: "operating",
  },
  {
    file: "UPGRADING.md",
    slug: "upgrading",
    title: "Upgrading polyemesis and its database",
    description:
      "Stop, replace the binary, start — the database migrates itself forward. What to back up first, the version-specific notes, and how to roll back.",
    section: "operating",
  },
  {
    file: "TROUBLESHOOTING.md",
    slug: "troubleshooting",
    title: "Troubleshooting: SRT, RTMP and audio problems",
    description:
      "Organised by what you can observe. SRT handshake failures, an ingest that never goes live, a destination producing nothing, and audio that is wrong.",
    section: "operating",
  },

  // -------------------------------------------------------------- automation
  {
    file: "MONITORING.md",
    slug: "monitoring",
    title: "Monitoring: Prometheus metrics and alerts",
    description:
      "Everything the dashboard draws, as metrics. The endpoint and why it needs authentication, metric conventions, queries to start from, and built-in alerts.",
    section: "automation",
  },
  {
    file: "HOOKS.md",
    slug: "hooks",
    title: "Lifecycle webhooks: one signed POST per event",
    description:
      "A signed JSON POST when a stream starts or stops and when a destination goes live or drops. The envelope, verifying a signature, ordering and gaps.",
    section: "automation",
  },
  {
    file: "MQTT.md",
    slug: "mqtt",
    title: "MQTT telemetry and Home Assistant",
    description:
      "Retained state on an MQTT 5.0 broker, so a consumer that was offline still gets the current answer. The topic tree, the payloads, and Home Assistant.",
    section: "automation",
  },
  {
    file: "API.md",
    slug: "api",
    title: "HTTP API reference — polyemesis /api/v1",
    description:
      "Everything the web UI does, it does through this API. Authentication, conventions, every route, the WebSocket, and a worked example end to end.",
    section: "automation",
  },

  // --------------------------------------------------------------- reference
  {
    file: "ARCHITECTURE.md",
    slug: "architecture",
    title: "Architecture: relay hub, engines, FFmpeg children",
    description:
      "Ingest once, fan out to N destinations. The process graph, the audio routing engine, the shared rendition encode, and the package layout.",
    section: "reference",
  },
  {
    file: "FAQ.md",
    slug: "faq",
    title: "polyemesis FAQ — self-hosted multistreaming",
    description:
      "What it is for, how it differs from Restreamer and restream.io, whether your video is re-encoded, how many destinations, and whether SRT is required.",
    section: "reference",
  },
  {
    file: "TESTING.md",
    slug: "testing",
    title: "Testing polyemesis without OBS or a camera",
    description:
      "FFmpeg only. Generate a multitrack SRT stream of distinct sine tones, route the tracks differently per destination, and measure what actually came out.",
    section: "reference",
  },
  {
    file: "DEPENDENCIES.md",
    slug: "dependencies",
    title: "Dependencies, and why each one is here",
    description:
      "Why every Go and frontend dependency is present, why some obvious alternatives are not, and why FFmpeg is a subprocess rather than a linked library.",
    section: "reference",
  },
  {
    /* CANONICALISED TO /comparison, AND KEPT OUT OF THE SITEMAP.
     *
     * The site already has a comparison page, and it and this document answer
     * one query — "restreamer alternative", "obs multi rtmp". Two indexed pages
     * on one intent do not double the chances; they split the signal and let a
     * search engine pick, which is the outcome neither page was written for.
     *
     * Publishing anyway, rather than dropping it, because the two are not the
     * same page doing the same job. /comparison is the ARGUMENT, written to be
     * read by someone deciding. This document is the EVIDENCE behind it: dated
     * sources, competitor tracker issue numbers, and the footnote recording that
     * a row said "Yes" until 2026-08-09 and was wrong. Five other documents link
     * to it; sending those links back out to github.com is the exact problem
     * rendering the docs here exists to fix.
     *
     * So: reachable, linked, indexed under /comparison's authority — and absent
     * from the sitemap, because submitting a URL that declares a different
     * canonical is a contradiction a crawler resolves by trusting neither. */
    file: "COMPARISON.md",
    slug: "comparison",
    title: "polyemesis vs Restreamer, restream.io, obs-multi-rtmp",
    description:
      "An honest comparison, including where polyemesis loses. Sourced from the competitors' own trackers and plan pages, with every claim dated.",
    section: "reference",
    canonical: "/comparison",
  },
];

/* NOT PUBLISHED, each with the reason — because "it is not in the other list"
 * is not a reason, and the next person deciding where a new file goes needs to
 * see what kind of thing lands here.
 *
 * The two in capitals are the ones that would actually hurt. The rest are
 * internal by nature rather than by risk: design notes for work not yet done,
 * process documents, and a changelog that is already published as releases.
 * @type {{ file: string, why: string }[]} */
export const NOT_PUBLISHED = [
  { file: "RESEARCH-COMPETITIVE.md", why: "COMPETITOR ANALYSIS. A survey of rivals' issue trackers, written for us." },
  { file: "COPY-CONSTRAINTS.md", why: "GOVERNS THE MARKETING COPY — what it may and may not claim, and why." },
  { file: "SITE-IMPROVEMENTS.md", why: "A work list for this website." },
  { file: "SITE-DEPLOY.md", why: "Deployment runbook for this website." },
  { file: "WEBSITE-COPY-PROPOSAL.md", why: "A draft of website copy, not documentation." },
  { file: "REVIEW-POKA-YOKE.md", why: "Internal review process." },
  { file: "TEST-STRATEGY.md", why: "Internal test strategy; TESTING.md is the user-facing half." },
  { file: "MODULES.md", why: "Internal package inventory; ARCHITECTURE.md is the user-facing half." },
  { file: "DESIGN-SYSTEM.md", why: "Design tokens for the app UI." },
  { file: "DESIGN-DESTINATION-HEALTH.md", why: "Design note for unshipped work." },
  { file: "DESIGN-ONE-PORT-ONLY.md", why: "Design note for unshipped work." },
  { file: "DESIGN-ONE-PORT-INGEST.md", why: "Design note for unshipped work." },
  { file: "CHANGES-SINCE-v0.6.0.md", why: "Release notes, already published on the releases page." },
  { file: "README.md", why: "A directory of the others; /docs is that page here." },
];

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
