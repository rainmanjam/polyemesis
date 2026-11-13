/* Sourced from docs/evidence/competitor-facts-2026-08-15.md, sections 3 and H,
 * and from nothing else.
 *
 * THE OVERCLAIM THAT IS BANNED HERE, same as on /vs/aitum-multistream:
 * obs-multi-rtmp DOES send different audio to different destinations. It selects
 * an OBS mixer track per output; the summing happened upstream in OBS. Writing
 * that it cannot is false.
 *
 * WHY THIS PAGE IS NOT THE AITUM PAGE WITH THE NAMES SWAPPED. The two plugins
 * have the same audio model, so if both pages argued about the mixing desk they
 * would be one page published twice -- thin to a crawler and duplicated to
 * SonarCloud. They differ elsewhere, and the difference is worth a page of its
 * own: obs-multi-rtmp's `Get from OBS` shares ONE encode across every
 * destination, which removes the CPU objection almost entirely and leaves the
 * uplink standing alone. So this page argues topology -- how many copies of the
 * broadcast leave the building -- and the Aitum page argues the mixing desk.
 *
 * The star count that used to appear in this project's copy is deliberately
 * absent: it is not in the facts file, and a number nobody re-checks is the kind
 * of claim this page cannot afford.
 *
 * THE CHECK DATE IS RENDERED, NOT MERELY DECLARED. web/scripts/check-build.mjs
 * reads the BUILT page for this, because two sibling pages already had a
 * CHECKED constant they never put on the screen -- which is a stamp only the
 * author can see.
 *
 * The `glance` and `fits` lists are SUMMARIES, and every cell in them is a
 * phrase lifted word for word from the page below or from docs/COMPARISON.md
 * (src/lib/vs-data.test.ts enforces it). Nothing in them is new.
 */

/** @type {import("../../lib/vs").VsData} */
export const DATA = {
  slug: "obs-multi-rtmp",
  title: "obs-multi-rtmp vs polyemesis: multistream from OBS",
  description:
    "The obs-multi-rtmp plugin adds one RTMP output per destination inside OBS. polyemesis fans out from a server. Both free — what changes is your uplink.",
  heading: "obs-multi-rtmp uploads once per platform.",
  accent: "once",
  lede: "The obs-multi-rtmp plugin adds a second, third and fourth RTMP output inside OBS, and each one is a separate upload from the machine you are broadcasting on. That is the whole shape of it, and for most people it is fine. This page is about the point where it stops being fine, which is your connection rather than your processor.",
  callout: {
    lead: "Give the plugin its due first:",
    body: "its <code>Get from OBS</code> option shares one encoder across every destination, so multi-RTMP in OBS does <em>not</em> cost you an encode per platform. The processor argument against it is mostly wrong. What it cannot share is the uplink: four destinations is the same bytes sent four times.",
  },
  checked: "15 August 2026",
  products: [{ key: "obs-multi-rtmp", name: "obs-multi-rtmp" }],
  glance: [
    [
      "Outbound streams from your connection",
      "One, to the server, regardless of how many destinations there are",
      "One per destination",
    ],
    [
      "Video encodes on the stream PC",
      "One, to produce the SRT ingest, and still one at ten destinations",
      "One, shared, with Get from OBS",
    ],
    [
      "Where a mix gets summed",
      "The server, per destination",
      "OBS's own audio mixer, upstream of the plugin",
    ],
    ["Cost", "Free, MIT, self-hosted", "Free, GPL-2.0"],
  ],
  fits: {
    poly: [
      "the fan-out happens somewhere with a better connection than a house",
      "a single 6 Mbps send",
      "The server stream-copies the picture through untouched",
    ],
    other: {
      heading: "obs-multi-rtmp fits when",
      items: [
        "Your upload comfortably carries every destination at once",
        "You want no infrastructure",
        "You are already running it and it works",
      ],
    },
  },
  sections: [
    {
      kind: "table",
      id: "leaves",
      heading: "What actually leaves the stream PC",
      intro:
        "The plugin runs inside OBS, so every destination it adds is another output from that machine. polyemesis is a server, so the machine sends one stream and the fan-out happens somewhere with a better connection than a house.",
      /* Header cell one is "What leaves the stream PC", NOT "Capability".
       * scripts/check-build.mjs treats any table headed "Capability" as a copy of
       * the canonical comparison table and requires every row of it to appear
       * verbatim in docs/COMPARISON.md. This table is not that table and should
       * not pretend to be -- it describes one machine's outbound load, which
       * COMPARISON.md does not cover. */
      caption: "What leaves the stream PC with obs-multi-rtmp and with polyemesis",
      head: ["What leaves the stream PC", "obs-multi-rtmp", "polyemesis"],
      rows: [
        [
          "Video encodes running on the stream PC",
          "One, shared, with Get from OBS, or one per destination when you want different bitrates",
          "One, to produce the SRT ingest, and still one at ten destinations. The server stream-copies the picture through untouched",
        ],
        [
          "Outbound streams from your connection",
          "One per destination",
          "One, to the server, regardless of how many destinations there are",
        ],
        [
          "Audio encodes on the stream PC",
          "One per destination",
          "One AAC encode per track you send, and that count is fixed by your track layout, not by the destination list. The summing and the per-destination encode happen on the server",
        ],
        [
          "Where a mix gets summed",
          "OBS's own audio mixer, upstream of the plugin",
          "The server, per destination, with a gain per channel-to-channel cell",
        ],
      ],
      after: [
        "Read the second row on a domestic upload. A 6 Mbps broadcast to four platforms is 24 Mbps sustained out of a connection that is also carrying the game, and every platform is receiving an identical copy. Moving the fan-out onto a server replaces that with a single 6 Mbps send, which is the argument for a server, and it is a bandwidth argument, not an audio one.",
        "It cuts the other way too, and belongs on the page: the server has to live somewhere, be patched, and be paid for. obs-multi-rtmp has no such bill and never goes down separately from OBS.",
      ],
    },
    {
      kind: "items",
      id: "audio",
      heading: "On the audio, precisely",
      items: [
        {
          h: "What it does",
          p: "Each output is assigned one of OBS's audio tracks (mixer IDs 1 through 6), and that track is what the destination receives. So different platforms genuinely can get different audio, and any comparison telling you otherwise is wrong about this plugin.",
        },
        {
          h: "Where the mix came from",
          p: "The plugin does not sum anything. Tracks 1 to 6 were summed by OBS's own mixer before the plugin saw them, so what you are choosing per destination is which of six pre-built mixes to send. polyemesis builds the mix per destination instead, from the tracks as they arrived.",
        },
      ],
      after: [
        "Six tracks is a real ceiling and also a generous one: most broadcasts need two. The distinction earns a page because of what it costs to change: a new mix in OBS is a new track layout, and it is not something to be editing while live.",
      ],
    },
  ],
  merits: {
    heading: "What obs-multi-rtmp does well",
    note: "Checked against the project's own repository and releases on <strong>15 August 2026</strong>.",
    items: [
      {
        h: "Actively maintained",
        p: "Developed by sorayuki, at v0.7.4.3 when this was checked, with regular compatibility releases tracking OBS Studio 30.x, 31.x and 32.x. In a plugin ecosystem where the usual failure is silent abandonment two OBS majors ago, that is the feature that matters most.",
      },
      {
        h: "Get from OBS is genuinely good engineering",
        p: "Sharing the main OBS encoder across every destination means fanning out to four platforms costs one encode, not four. This is the same idea as a shared rendition on a server, implemented locally, and it is the reason the CPU argument against this plugin is mostly wrong.",
      },
      {
        h: "Independent re-encoding when you want it",
        p: "Separate encoder instances per destination, with their own bitrates and resolutions, for the platform that will not take your source.",
      },
      {
        h: "Free, GPL-2.0, and inside OBS already",
        p: "No cost, no account, no server, no second machine. It ingests whatever OBS ingests and publishes over RTMP and RTMPS.",
      },
    ],
  },
  useOther: {
    heading: "Use the plugin, not this",
    items: [
      "Your upload comfortably carries every destination at once. If it does, the main reason to move the fan-out off your machine has already been answered.",
      "The per-destination mixes you want are ones you can lay out across OBS's six tracks. Selecting one of those per output is exactly what this plugin does, at zero cost.",
      "You want no infrastructure. A plugin that ships with your broadcast software is one fewer thing to keep alive.",
      "You are already running it and it works. There is no upgrade being sold here.",
    ],
  },
  footer:
    "Everything this page says about obs-multi-rtmp was checked on <strong>15 August 2026</strong>, and is recorded in <code>docs/evidence/competitor-facts-2026-08-15.md</code>. Prices and plan limits move; check before deciding on the strength of a number here.",
  closing: { heading: "Still weighing it up?" },
};
