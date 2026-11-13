/* Sourced from docs/evidence/competitor-facts-2026-08-15.md section 8, added
 * 2026-08-16, and nothing else. That section was written before this page, from
 * streamlabs.com/multistream and its own FAQ block, read the same day.
 *
 * WHY THIS PAGE IS NOT SHAPED LIKE THE PLUGIN PAGES. obs-multi-rtmp, Aitum and
 * SE.Live were read at the source level, so their track-SELECTION behaviour is
 * known and the narrow true claim about audio could be written. Streamlabs
 * Multistream is a cloud relay: the fan-out happens on their servers and there
 * is no source to read. Their page says NOTHING about per-destination audio.
 *
 * So this page does not say they cannot do it. Under the rule at the head of the
 * facts file, absence of a claim on a marketing page is not evidence of absence
 * in a product, and the honest treatment is to state what polyemesis does and
 * leave their side of that row unasserted. That is deliberate and a reviewer
 * should not "fix" it by filling the gap in.
 *
 * NO PRICE APPEARS HERE. Ultra's price was not read on 2026-08-16 and a
 * subscription figure is the most movable fact there is. "Requires the paid
 * Ultra tier" is what the source supports; a number is not.
 *
 * The real contrast is architectural and it is theirs, in their words: "We
 * process your stream on our servers". That is the sentence this whole page
 * turns on, and it is quoted rather than paraphrased.
 *
 * NO `merits` BLOCK. The page as it was had no "what they do well" section: what
 * Streamlabs does well was one sentence inside "Use Streamlabs, not this", and it
 * stays there, whole. Splitting it into cards would have meant writing new
 * headings over it, so the page does not pretend to a section it never had.
 *
 * The `glance` and `fits` lists are SUMMARIES, and every cell in them is a
 * phrase lifted word for word from the page below or from docs/COMPARISON.md
 * (src/lib/vs-data.test.ts enforces it). Nothing in them is new.
 */

/** @type {import("../../lib/vs").VsData} */
export const DATA = {
  slug: "streamlabs",
  title: "Streamlabs Multistream vs polyemesis: cloud or your own box",
  description:
    "Streamlabs relays from their servers and gates multistreaming behind Ultra. polyemesis is a relay you run yourself, with a different mix per destination.",
  heading: "Streamlabs multistreams from their servers. polyemesis multistreams from yours.",
  accent: "their",
  lede: "Both send one upload to several platforms. The difference is whose hardware does the fan-out, whether it costs a subscription, and what each destination is allowed to hear. Checked against Streamlabs' own documentation on 16 August 2026.",
  checked: "16 August 2026",
  products: [{ key: "streamlabs", name: "Streamlabs" }],
  glance: [
    ["Where the fan-out runs", "Yours", "Their servers"],
    ["Price", "Free, MIT, self-hosted", "Paid: an Ultra feature"],
    ["Ingest", "multitrack SRT in", "RTMP in"],
    ["Destinations on the free path", "no cap on destinations", "Free, and capped at two"],
  ],
  fits: {
    poly: [
      "the archive needs a different mix from the broadcast",
      "the stream must not pass through a third party",
      "a subscription per month for a fan-out you could run yourself is the wrong shape",
    ],
    other: {
      heading: "Streamlabs fits when",
      items: [
        "you want multistreaming without running a server",
        "one mix suits every platform you send to",
      ],
    },
  },
  which: {
    heading: "Which Streamlabs are you using?",
    verdict: "foot",
    intro:
      "Streamlabs ships two ways to reach more than one platform, and they are not the same product decision.",
    items: [
      {
        name: "Multistream",
        verdict: "Paid: an Ultra feature",
        body: "The cloud fan-out. Twitch, YouTube, TikTok, Kick, Facebook, Patreon, X and custom RTMP.",
        outcome: "Your stream goes to Streamlabs and their servers relay it onward. Usable from Streamlabs Desktop, the Streamlabs plugin for OBS, or any encoder via an RTMP URL from their dashboard.",
      },
      {
        name: "Dual Output",
        verdict: "Free, and capped at two",
        body: "Two canvases (one vertical, one horizontal) in Streamlabs Desktop.",
        outcome: "Their words: “Stream to one platform per canvas, completely free.” It is the free multistreaming path, and its ceiling is two destinations of prescribed shapes rather than a number you choose.",
      },
    ],
  },
  sections: [
    {
      kind: "items",
      id: "differences",
      heading: "The four differences that matter",
      items: [
        {
          h: "Their servers, or yours",
          p: "“We process your stream on our servers so you can expand your reach without straining your PC.” That is the design, stated on their own page, and for most people it is the right trade: one upload, no hardware. polyemesis makes the opposite trade: the relay is a binary on a box you own, so nothing leaves your network except the streams you are publishing.",
        },
        {
          h: "A subscription, or not",
          p: "Multistream proper is an Ultra feature. The free tier is Dual Output and stops at two destinations. polyemesis is MIT and has no paid tier and no cap on destinations: the limit is your upstream bandwidth and roughly 4% of one CPU core per destination, which is a physical limit rather than a billing one.",
        },
        {
          h: "RTMP in, or multitrack SRT in",
          p: "Streamlabs takes an RTMP stream, from their desktop app, their OBS plugin, or any encoder pointed at their dashboard URL. polyemesis ingests SRT carrying up to 32 audio tracks on one connection, with RTMP as a single-track fallback. The multitrack ingest is what the per-destination mixing is built on; without it there is nothing to mix from.",
        },
        {
          h: "Where the audio decision happens",
          p: "polyemesis sums tracks server-side and gives every destination its own stereo mix: the full mix to one platform, a music-free mix to another, a mic-only feed to a third, from one upload and one video encode. Streamlabs' page does not describe per-destination audio either way, so nothing is claimed about it here; if that is the thing you need, it is a question worth putting to them directly.",
        },
      ],
    },
  ],
  useOther: {
    heading: "Use Streamlabs, not this",
    items: [
      "If you want multistreaming without running a server, Streamlabs is the easier answer and this project is not competing for that.",
      "Their relay costs you one upload and no hardware, their desktop app and OBS plugin are built for the job, and Multichat reads and posts across YouTube, Twitch and Facebook Pages from one place.",
      "If one mix suits every platform you send to, you are already done.",
    ],
    after: [
      "polyemesis is for the case where that is the thing that does not work: where the archive needs a different mix from the broadcast, where the stream must not pass through a third party, or where a subscription per month for a fan-out you could run yourself is the wrong shape.",
    ],
    links: [
      { label: "The three ways to multistream", href: "/multistream" },
      { label: "The full comparison", href: "/comparison" },
      { label: "How the mix works", href: "/docs/audio-routing" },
    ],
  },
  footer:
    "Everything this page says about Streamlabs was checked on <strong>16 August 2026</strong>, and is recorded in <code>docs/evidence/competitor-facts-2026-08-15.md</code>. Prices and plan limits move; check before deciding on the strength of a number here.",
  closing: { heading: "Weighing up the others too?" },
};
