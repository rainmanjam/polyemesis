/* Sourced from docs/evidence/competitor-facts-2026-08-15.md, section 5, and
 * nothing else. That section was added after this page was first refused: the
 * facts did not exist, so the page was not written, which is the only correct
 * order for these.
 *
 * THE OVERCLAIM WARNING APPLIES IN FULL. SE.Live SELECTS an OBS audio track per
 * platform output and cannot sum; the summing happened upstream in Advanced
 * Audio Properties. Same case as obs-multi-rtmp and Aitum. "It cannot do
 * per-destination audio" is false and is not written here.
 *
 * WHICH IS EXACTLY WHY THE AUDIO SECTION ON THIS PAGE IS SHORT AND LINKS OUT.
 * The argument has been made at length twice already. Making it a third time
 * would be the same page published again -- thin to a crawler, duplicated to
 * SonarCloud, and boring to the one reader who has clicked all three. So this
 * page states the fact once, sends anyone who wants it argued to the pages that
 * argue it, and spends its own length on the two things that are true of
 * SE.Live and of nothing else on this site:
 *
 *   1. Windows only. macOS and Linux are not supported. No other tool compared
 *      anywhere on this site has a hard platform stop.
 *   2. RTMP/RTMPS egress only, and no ingest at all -- it is not a relay. SRT is
 *      not supported, multitrack SRT least of all. On the other plugin pages the
 *      ingest question was a footnote; here it settles the matter outright,
 *      because a multitrack workflow cannot start.
 *
 * The disambiguation is also NOT the one on /vs/restreamer, and the difference
 * matters. There, two unrelated companies shipped confusingly similar names.
 * Here one company ships two things, and the famous one -- the overlays and
 * chat dashboard everybody has heard of -- does not multistream video at all.
 *
 * The `which` block is a verdict per product rather than a feature card,
 * because the question a reader actually arrives with is "does the
 * StreamElements I already use do this", and that is answerable yes or no
 * before any comparing starts.
 *
 * The `glance` and `fits` lists are SUMMARIES, and every cell in them is a
 * phrase lifted word for word from the page below or from docs/COMPARISON.md
 * (src/lib/vs-data.test.ts enforces it). Nothing in them is new.
 */

/** @type {import("../../lib/vs").VsData} */
export const DATA = {
  slug: "streamelements",
  title: "StreamElements multistream: SE.Live vs polyemesis",
  description:
    "StreamElements multistreaming means SE.Live, a Windows-only OBS plugin. It selects an OBS audio track per platform and sends no SRT at all.",
  heading: "StreamElements multistreaming is SE.Live. It runs on Windows only.",
  accent: "Windows",
  lede: "Anyone looking into StreamElements multistream support meets a fork in the road immediately, and it is not signposted well: the StreamElements almost everybody knows (overlays, tips, the chat dashboard) sends no video anywhere. The multistreaming lives in a separate OBS plugin called SE.Live, and before comparing anything it is worth knowing which one you are holding.",
  callout: {
    lead: "Two facts settle most of this page:",
    body: "SE.Live is supported on Windows and not on macOS or Linux, and it publishes over RTMP and RTMPS with no SRT support of any kind. The first rules it out for a lot of people outright. The second means the multitrack workflow polyemesis is built around cannot begin.",
  },
  checked: "15 August 2026",
  products: [{ key: "se-live", name: "SE.Live" }],
  glance: [
    ["Transport", "multitrack SRT", "RTMP and RTMPS out"],
    ["Audio per platform", "a mix assembled per platform", "one OBS audio track (any of the six)"],
    ["Price", "Free, MIT, self-hosted", "Free at $0 with no paywall above it"],
  ],
  fits: {
    poly: [
      "you are not on Windows",
      "you want a mix assembled per platform rather than chosen from what OBS prepared",
      "you want the fan-out to leave a server instead of your own connection",
    ],
    other: {
      heading: "SE.Live fits when",
      items: [
        "you broadcast from Windows and one of the six OBS tracks already carries the right sound for each platform",
        "you are already inside the StreamElements ecosystem for overlays, tips and chat",
        "you are on Windows and do not want to run a server",
      ],
    },
  },
  which: {
    heading: "Which StreamElements do you have?",
    intro: "One company, two products, and only one of them touches video.",
    items: [
      {
        name: "StreamElements Cloud",
        verdict: "No video multistreaming",
        body: "The web dashboard. Unified multi-platform chat, activity feeds, overlays and tipping.",
        outcome: "If this is the StreamElements you use, it does not restream video anywhere, and nothing on this page is a comparison with it.",
      },
      {
        name: "SE.Live",
        verdict: "This is the multistreaming one",
        body: "Formerly OBS.Live. A plugin that installs into OBS Studio on your own machine.",
        outcome: "Everything below is about this. It is free, and it is the only video multistreaming StreamElements ships.",
      },
    ],
  },
  sections: [
    {
      kind: "items",
      id: "limits",
      heading: "Two limits worth checking before anything else",
      intro:
        "Neither is a matter of degree, which is why they come first. Checked against StreamElements' own documentation on <strong>15 August 2026</strong>.",
      items: [
        {
          h: "Operating system",
          p: "Windows, and only Windows. macOS and Linux are not supported.",
          more: "Nothing else compared on this site has a stop like this: the other plugins follow OBS wherever it runs, and the servers are containers. If you broadcast from a Mac, the comparison ends at this paragraph.",
        },
        {
          h: "Transport",
          p: "RTMP and RTMPS out. No SRT, and no ingest of any kind: it captures from the OBS it lives inside and is not a relay.",
          more: "This is the cleanest version of the difference on any of these pages. polyemesis works by receiving your separated audio tracks over multitrack SRT and deciding what each platform hears from them. SE.Live has no way to send that, so the question of what to do with those tracks never arises.",
        },
      ],
    },
    {
      kind: "prose",
      id: "audio",
      heading: "On the audio, it is the same story as the other two plugins",
      paragraphs: [
        "SE.Live routes one OBS audio track (any of the six) to each platform output. So yes, it can give Twitch and YouTube different sound, and a comparison claiming otherwise would be wrong. What it cannot do is combine tracks into a new mix at the output; whatever those six tracks contain was decided earlier, in OBS's Advanced Audio Properties.",
        "That is precisely the model obs-multi-rtmp and Aitum Multistream use, and it is argued at length on both of their pages rather than repeated here.",
        'The same argument, in full: <a href="/vs/aitum-multistream">Aitum Multistream</a> · <a href="/vs/obs-multi-rtmp">obs-multi-rtmp</a>',
      ],
    },
  ],
  merits: {
    heading: "What SE.Live does well",
    note: "Checked against StreamElements' own documentation and site on <strong>15 August 2026</strong>.",
    items: [
      {
        h: "No cap on destinations, and no paid tier",
        p: "Free at $0 with no paywall above it, and no ceiling on how many platforms you publish to: the limit is your encoding capacity and your upstream bandwidth, which is an honest limit rather than a billing one.",
      },
      {
        h: "Canvas does vertical and horizontal at once",
        p: "Its Canvas system produces simultaneous 16:9 and 9:16 output from a single OBS session, with Twitch Dual-Output Beta wired in. polyemesis reaches a vertical output differently: a 9:16 rendition, encoded once and shared, that crops, letterboxes or blur-fills the frame you already sent. What it cannot do is lay out a separate vertical scene; if the vertical cut needs its own composition, that is a reason to run SE.Live.",
      },
      {
        h: "Per-destination encoding, with aspect ratio in the mix",
        p: "Shared copy or an independent re-encode per platform, each with its own resolution, aspect, bitrate, framerate and encoder. Those re-encodes run concurrently on the local machine.",
      },
      {
        h: "GPL-2.0 core, actively supported",
        p: "The core plugin is GPL-2.0 and openly published; the cloud backend it connects to is proprietary. Nothing about it was deprecated in the year to this check.",
      },
    ],
  },
  useOther: {
    heading: "Use SE.Live, not this",
    items: [
      "You broadcast from Windows and one of the six OBS tracks already carries the right sound for each platform.",
      "You want the vertical cut composed as its own scene rather than reframed from the horizontal one. Canvas does that in a single OBS session; a polyemesis rendition can only crop, letterbox or blur-fill the frame it received.",
      "You are already inside the StreamElements ecosystem for overlays, tips and chat, and adding a separate server to the setup buys you nothing you asked for.",
      "You are on Windows and do not want to run a server. SE.Live needs no host, no port and no certificate; polyemesis needs all three before it sends a byte.",
    ],
    after: [
      "polyemesis is the answer to a narrower question: you are not on Windows, or you want a mix assembled per platform rather than chosen from what OBS prepared, or you want the fan-out to leave a server instead of your own connection.",
    ],
  },
  footer:
    "Everything this page says about StreamElements was checked on <strong>15 August 2026</strong>, and is recorded in <code>docs/evidence/competitor-facts-2026-08-15.md</code>. Prices and plan limits move; check before deciding on the strength of a number here.",
  closing: { heading: "Weighing up the others too?" },
};
