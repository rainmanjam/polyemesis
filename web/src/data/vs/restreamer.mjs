/* Sourced from docs/evidence/competitor-facts-2026-08-15.md, sections 1 and 2.
 *
 * THIS PAGE IS THE ONE WHERE THE STRONG AUDIO CLAIM IS TRUE, and keeping that
 * straight is the reason it is a separate page rather than a fourth column
 * somewhere.
 *
 * On /vs/aitum-multistream and /vs/obs-multi-rtmp the copy is careful to say the
 * plugins DO send different audio per destination -- they select an OBS track,
 * and the summing happened upstream. Here that qualification does not apply:
 * per the facts file, neither Restreamer nor restream.io can produce a different
 * mix per destination at all. Restreamer can copy, transcode the codec, or force
 * silence; restream.io duplicates one stereo track everywhere.
 *
 * Flattening all four competitors into "they cannot do what we do" would be
 * wrong about two of them. Flattening the other way -- hedging here because we
 * hedged there -- would be underselling a difference that is real. Two pages,
 * two claims, both accurate.
 *
 * WHY THE DISAMBIGUATION IS THE SPINE. The keywords this page answers are
 * "restream alternative", "restream alternatives" and "restreamer alternative",
 * and they are three different intentions: datarhei Restreamer is a free
 * self-hosted Apache-2.0 server, restream.io is a paid SaaS, and a reader who
 * typed one of those phrases may well have meant the other. Sorting that out is
 * genuinely the most useful thing this page can do, and it is also what keeps it
 * from being the plugin pages again with different nouns.
 *
 * EVERY PRICE CARRIES A VISIBLE CHECKED DATE. restream.io's tiers move, and an
 * unstamped figure is a false statement about a competitor's pricing -- a
 * different class of problem from a stale docs page.
 *
 * The `glance` and `fits` lists are SUMMARIES, and every cell in them is a
 * phrase lifted word for word from the page below or from docs/COMPARISON.md
 * (src/lib/vs-data.test.ts enforces it). Nothing in them is new.
 *
 * Three columns, not four: this is the one page that covers two products, so it
 * is the one page whose table has two competitor columns and whose chooser has
 * two boxes. Unticking one leaves polyemesis and the other.
 */

/** @type {import("../../lib/vs").VsData} */
export const DATA = {
  slug: "restreamer",
  title: "Restream alternatives: Restreamer, restream.io, polyemesis",
  description:
    "Two different products are called Restream. One is self-hosted and Apache-2.0, one is a subscription — and neither can mix audio per destination.",
  heading: "Two products are called Restream. Neither gives a platform its own mix.",
  accent: "Neither",
  lede: "Almost everyone searching for a restream alternative means one of two unrelated things, and the answers are not the same. A Restreamer alternative is a replacement for datarhei's free, self-hosted, Apache-2.0 server. A restream.io alternative is a replacement for a subscription. This page separates them, then makes the one argument that applies to both.",
  checked: "15 August 2026",
  products: [
    { key: "restreamer", name: "Restreamer" },
    { key: "restream-io", name: "restream.io" },
  ],
  glance: [
    ["Per-destination audio mix", "Yes", "No", "No"],
    ["Self-hosted", "Yes", "Yes", "No"],
    ["Price", "Free, MIT, self-hosted", "Free, Apache-2.0, self-hosted", "Free tier, then $19/mo"],
    ["Simultaneous destinations", "No configured cap", "not assessed", "2–5 by plan tier (checked 15 Aug 2026)"],
  ],
  fits: {
    poly: [
      "the clean feed goes to one platform, the full mix to the next, from one upload",
      "the picture is copied through rather than re-encoded to do it",
    ],
    other: {
      heading: "Restreamer or restream.io fits when",
      items: [
        "one mix to every platform is what you need",
        "restream.io needs no server from you",
        "Restreamer is free, Apache-2.0 and complete",
      ],
    },
  },
  which: {
    heading: "Which one did you mean?",
    items: [
      {
        name: "datarhei Restreamer",
        id: "github.com/datarhei/restreamer",
        body: "A complete self-hosted restreaming server you run yourself. Ingests SRT, RTMP, RTMPS, RTSP and HLS, and publishes to as many destinations as your hardware carries.",
        facts: [
          ["Kind", "Self-hosted server, Docker"],
          ["Licence", "Apache-2.0"],
          ["Price", "Free"],
        ],
      },
      {
        name: "restream.io",
        id: "restream.io",
        body: "A cloud service that takes one stream and replicates it to your platforms, with a browser studio on top. Nothing to run, and a per-channel bill.",
        facts: [
          ["Kind", "Hosted service, subscription"],
          ["Licence", "Proprietary"],
          ["Price", "Free tier, then $19/mo"],
        ],
      },
    ],
  },
  sections: [
    {
      kind: "items",
      id: "second-mix",
      heading: "Neither can build a second mix",
      intro:
        'This is the sharpest version of the argument on this site, and it is worth being exact about why. The OBS plugins people also weigh up, <a href="/vs/obs-multi-rtmp">obs-multi-rtmp</a> and <a href="/vs/aitum-multistream">Aitum Multistream</a>, genuinely do send different audio to different platforms, by selecting a track OBS mixed for them. Neither Restreamer nor restream.io can do even that.',
      items: [
        {
          h: "Restreamer",
          p: "Per egress target the web UI offers three choices: copy the primary stream's audio, transcode the codec (AAC or MP3), or force silence. There is no matrix for summing discrete channels into a new stereo mix. Re-routing multiple tracks is possible, but only by writing FFmpeg pipelines by hand against the underlying REST API, which is a different product than the one the UI presents.",
        },
        {
          h: "restream.io",
          p: "It cannot sum tracks and it cannot select between them. The single stereo track you send is duplicated identically to every enabled endpoint. There is no per-destination audio setting because there is no second piece of audio for one to choose from.",
        },
      ],
      after: [
        "polyemesis takes a multitrack ingest and sums a mix per destination, with a gain on each input-channel-to-output-channel cell and a loudness target measured after the routing rather than before it. The clean feed goes to one platform, the full mix to the next, from one upload, and the picture is copied through rather than re-encoded to do it.",
      ],
    },
    {
      kind: "items",
      id: "ingest",
      heading: "The reason is upstream, at the ingest",
      intro:
        "Neither product is missing a mixer by oversight. Neither can route a multitrack input in the first place, so there is nothing for a mixer to work on.",
      items: [
        {
          h: "Restreamer",
          p: "Ingests SRT, RTMP, RTMPS, RTSP and HLS. SRT arrives with a single audio track in the UI, and multitrack SRT is neither exposed nor routable there.",
        },
        {
          h: "restream.io",
          p: "Accepts RTMP, RTMPS and SRT. Multitrack SRT is not supported at all: a single stereo stream is the input contract.",
        },
        {
          h: "polyemesis",
          p: "Takes the split audio tracks OBS already knows how to send over multitrack SRT and keeps them separate all the way to the point where a destination asks for a mix.",
        },
      ],
    },
    {
      kind: "table",
      id: "cost",
      heading: "What each one costs",
      intro:
        "Every figure below was read from the vendor's own pricing page on <strong>15 August 2026</strong>. Tiers move. Check before deciding on the strength of a number here.",
      caption: "Pricing for Restreamer, restream.io and polyemesis, checked on 15 August 2026",
      /* Header cell one is deliberately not "Capability": scripts/check-build.mjs
       * requires every row of a table headed that way to appear verbatim in
       * docs/COMPARISON.md, which is the canonical five-product table and does not
       * carry pricing. */
      head: ["Plan", "Price checked 15 August 2026", "Destinations", "What is included"],
      rows: [
        ["Restreamer (datarhei)", "Free, Apache-2.0, self-hosted", "—", "No per-destination charge"],
        [
          "restream.io Free",
          "$0",
          "2 channels",
          "Personal profiles only. No custom RTMP, no Facebook Pages or Groups. Studio capped at 720p and watermarked",
        ],
        [
          "restream.io Standard",
          "$19/mo, or $16/mo billed annually at $190/yr",
          "3 channels",
          "No watermark. Custom RTMP destinations enabled",
        ],
        [
          "restream.io Professional",
          "$49/mo, or $39/mo billed annually at $470/yr",
          "5 channels",
          "1080p Studio. Dual Format: horizontal and vertical output together",
        ],
        ["polyemesis", "Free, MIT, self-hosted", "No configured cap", "Your hardware and your bandwidth are the bill"],
      ],
      after: [
        "The free-tier exclusion worth knowing before you sign up: restream.io's free plan does not include custom RTMP destinations, which is the setting anyone sending to a self-hosted or niche endpoint needs.",
      ],
    },
  ],
  merits: {
    heading: "What they do well",
    items: [
      {
        h: "Restreamer is free, Apache-2.0 and complete",
        p: "Not a trial or an open core. It ships as a Docker container, ingests over five protocols, and does per-destination re-encoding in software or hardware when a platform will not take your source.",
      },
      {
        h: "Restreamer shares one ingest across destinations",
        p: "Several destination processes pull from the single ingested internal stream without re-encoding it: the same shared-rendition idea polyemesis uses, and it has had it longer.",
      },
      {
        h: "restream.io needs no server from you",
        p: "That is a real advantage and no self-hosted tool answers it. There is nothing to provision, patch, monitor or pay a host for, and the fan-out runs on a connection far better than yours.",
      },
      {
        h: "restream.io keeps shipping",
        p: "In the twelve months to 15 August 2026 it added Dual-Format streaming (separate horizontal and vertical outputs at once) and automated AI clip generation.",
      },
    ],
    after: [
      "Restreamer's stable line was at v2.12.0 when this was checked, with ongoing dependency and container maintenance. If one mix to every platform is what you need, it does that job for nothing and has done for years, and it is the tool this project would point you at.",
    ],
  },
  closing: {
    heading: "Different audio per platform?",
    lede: "That is the one thing neither of them does, and the reason this project exists.",
  },
};
