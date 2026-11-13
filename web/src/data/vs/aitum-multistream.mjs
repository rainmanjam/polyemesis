/* EVERY CLAIM ABOUT AITUM ON THIS PAGE TRACES TO docs/evidence/competitor-facts-2026-08-15.md,
 * section 4, and to nothing else. That file is the only sanctioned source, and
 * the reason is the paragraph it opens with:
 *
 *   Aitum Multistream DOES send different audio to different destinations.
 *
 * It assigns an OBS audio track per output, and the summing that produced those
 * tracks happened upstream in OBS's Advanced Audio Properties. So "Aitum cannot
 * do per-destination audio" is FALSE and must never appear here in any wording.
 * The true argument is narrower and better, and it is the one this page makes:
 * they select a mix OBS has already built, so N different mixes means N track
 * layouts maintained in OBS and N encodes on the stream PC.
 *
 * That is also why this page is a WALKTHROUGH rather than a table. The
 * difference is not a capability either product lacks -- it is how much work the
 * same finished result costs, and a Yes/No grid cannot show that. It would also
 * have made this page a near-copy of /vs/obs-multi-rtmp, whose argument is about
 * the uplink rather than the mixing desk.
 *
 * THE CHECK DATE IS RENDERED, NOT MERELY DECLARED. web/scripts/check-build.mjs
 * reads the BUILT page for this, because two sibling pages already had a
 * CHECKED constant they never put on the screen -- which is a stamp only the
 * author can see.
 *
 * The page carries two dates and the old page did too: "16 September 2026" in
 * the footer (`checked`, the one the build reads) and "15 August 2026" in the
 * note under "What Aitum Multistream does well". Both are kept as they were.
 *
 * The `glance` and `fits` lists are SUMMARIES, and every cell in them is a
 * phrase lifted word for word from the page below or from docs/COMPARISON.md
 * (src/lib/vs-data.test.ts enforces it). Nothing in them is new.
 */

/** @type {import("../../lib/vs").VsData} */
export const DATA = {
  slug: "aitum-multistream",
  title: "Aitum Multistream vs polyemesis: the audio model",
  description:
    "The Aitum Multistream OBS plugin assigns one OBS audio track per destination. polyemesis sums tracks server-side. Both free — here is what differs.",
  heading: "Aitum Multistream picks a mix. polyemesis builds one.",
  accent: "picks",
  lede: "The Aitum Multistream OBS plugin can already send different audio to different platforms; anyone telling you otherwise has not used it. Each destination is assigned one of OBS's six audio tracks, and that track goes out. The question this page answers is a narrower one: where the mix on that track gets made, and what it costs you to have more than one.",
  callout: {
    lead: "The difference in one sentence:",
    body: "Aitum selects a mix OBS has already built, so three different mixes means three track layouts maintained in Advanced Audio Properties and each output encoded on the stream PC. polyemesis sums the tracks server-side, so one upload and one video encode feed every destination.",
  },
  checked: "16 September 2026",
  products: [{ key: "aitum-multistream", name: "Aitum Multistream" }],
  glance: [
    ["Where the mix is made", "The server", "OBS's Advanced Audio Properties"],
    [
      "Video encodes",
      "The picture is copied through rather than re-encoded",
      "one per destination unless you clone the main output",
    ],
    [
      "Uploads from your connection",
      "One upload leaves the stream PC",
      "Every destination uploads from your connection",
    ],
    ["Cost (checked 15 August 2026)", "Free, MIT, self-hosted", "Free, and GPL-2.0"],
  ],
  fits: {
    poly: [
      "the mixes outnumber the tracks",
      "the stream PC has no headroom left to encode with",
      "the upload is the ceiling",
    ],
    other: {
      heading: "Aitum Multistream fits when",
      items: [
        "one mix suits every platform",
        "your track layout is settled and you are not editing it under load",
        "you do not want to run, patch or pay for a server",
      ],
    },
  },
  sections: [
    {
      kind: "steps",
      id: "walkthrough",
      heading: "The same broadcast, set up both ways",
      intro:
        "Two destinations. Twitch takes the music, YouTube does not. Both tools finish this job, and comparing the routes is more useful than comparing a column of Yes against a column of No.",
      /* The same job, done twice. Deliberately the modest version of the task --
       * two destinations, one difference between them -- because the honest thing
       * about Aitum is that at this size it is fine, and the cost only compounds
       * later. */
      columns: [
        {
          title: "In OBS, with Aitum Multistream",
          where: "Every step is on the stream PC.",
          steps: [
            "Open Advanced Audio Properties and build the layout: mic and game on Track 1, mic and game and music on Track 2. This is the mix, and this is where it is made.",
            "Add the Twitch destination in Aitum, set its audio track to 2.",
            "Add the YouTube destination, set its audio track to 1.",
            "Choose an encoder for each destination, or clone the main OBS output for both.",
            "Both destinations now upload from this machine.",
          ],
        },
        {
          title: "In polyemesis",
          where: "One upload leaves the stream PC. The rest happens on the server.",
          steps: [
            "In OBS, put each source on its own track: mic on 1, game on 2, music on 3. No layout to design, because nothing is being summed yet.",
            "Send one multitrack SRT stream to polyemesis.",
            "Tick the tracks each destination should hear: Twitch gets 1, 2 and 3; YouTube gets 1 and 2.",
            "Change your mind mid-broadcast by unticking a box. The track layout in OBS never moves.",
          ],
        },
      ],
      after: [
        "At two destinations these are the same amount of work, and the honest summary is that it does not matter which you pick. The divergence starts at the third: a new mix in Aitum is a new track layout in OBS, and OBS has six tracks. In polyemesis a new mix is a row of checkboxes, and the thing being ticked is the ingest, which has not changed.",
      ],
    },
    {
      kind: "items",
      id: "work-lands",
      heading: "Where the work lands",
      intro:
        "Not a capability difference. A location difference, and on a machine that is also running the game, location is the whole argument.",
      items: [
        {
          h: "Aitum Multistream",
          p: "The mixing desk is OBS's Advanced Audio Properties. The encoders run on the stream PC, one per destination unless you clone the main output. Every destination uploads from your connection.",
        },
        {
          h: "polyemesis",
          p: "The mixing desk is the server, with a gain per input-channel-to-output-channel cell and a loudness target measured after the routing. One upload leaves the stream PC; the picture is copied through rather than re-encoded.",
        },
      ],
      after: [
        "The cost belongs here too, because it is real: mixing means decoding the audio and encoding it again for every destination, where selecting a track is a copy. polyemesis also needs a server, and Aitum does not. If the mixes you want are ones OBS can already build on its six tracks, the plugin is free and you should use it.",
      ],
    },
  ],
  /* Stated because a comparison page that finds no merit in the other tool reads
   * as marketing, and this audience checks. Every line traces to section 4. */
  merits: {
    heading: "What Aitum Multistream does well",
    note: "Checked against Aitum's own repository and site on <strong>15 August 2026</strong>. The $5.49 Nexus figure below was re-checked on 16 September 2026.",
    items: [
      {
        h: "Free, and GPL-2.0",
        p: "The Multistream plugin costs nothing. Aitum sells a separate desktop automation suite, Nexus, at $5.49/mo, and Multistream does not require it.",
      },
      {
        h: "Per-destination encoding",
        p: "Each destination can clone the main OBS output or re-encode independently, with its own resolution, bitrate and encoder. That is not a lesser feature than what a server does. It is the same feature, paid for locally.",
      },
      {
        h: "It is where you already are",
        p: "It runs inside OBS. There is no server to provision, no second machine, no ingest to point anywhere. For a great many broadcasts that is the correct architecture and this page is not an argument against it.",
      },
      {
        h: "StreamSuite",
        p: "Multistream is now bundled with Aitum Vertical under the StreamSuite name, with unified multi-chat and metadata panels alongside it.",
      },
    ],
  },
  useOther: {
    heading: "Use Aitum, not this",
    items: [
      "One mix suits every platform. This is most broadcasts, and adding a server to serve it is a worse setup, not a better one.",
      "Your track layout is settled and you are not editing it under load.",
      "You want a destination at a different resolution or bitrate and your PC has the headroom to encode it. Aitum does this natively.",
      "You do not want to run, patch or pay for a server. That is a legitimate position and there is no answer to it here.",
    ],
    after: [
      "polyemesis is worth the server when the mixes outnumber the tracks, when the stream PC has no headroom left to encode with, or when the upload is the ceiling: one stream out instead of one per platform.",
    ],
  },
  footer:
    "Everything this page says about Aitum was checked on <strong>16 September 2026</strong>, and is recorded in <code>docs/evidence/competitor-facts-2026-08-15.md</code>. Prices and plan limits move; check before deciding on the strength of a number here.",
  closing: { heading: "Weighing up the others too?" },
};
