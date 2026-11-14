/* The /tour page: its metadata and the text for each chapter of the film.
 *
 * The film (data/tour.mjs) is a recording of a demo rig, not of real platforms:
 * its destinations are labelled as YouTube channels but publish to a local
 * receiver, and it never shows OBS, a sign-in or a platform's own page. So each
 * note says only what the frames of its chapter show AND what the docs say the
 * console does, and no note describes a real account being connected or a real
 * channel going live. Every sentence is a Fact (lib/facts.ts): the doc it
 * comes from and the phrases that prove it, checked by tour-page.test.ts.
 *
 * What each chapter shows (the frames of the 98 s film, v0.18.0):
 *  1 One stream in (0:00): the Ingest panel live with an srt:// address,
 *    its bitrate and 6 audio tracks; the Routing page with a meter for each of
 *    six tracks, five commentary languages and the crowd.
 *  2 Splitting the audio (0:22): the Routing overview with every destination
 *    ticked on every track, then one destination's tracks being unticked, then
 *    one language per destination with the crowd on all five; an unsaved-change
 *    bar.
 *  3 Going live everywhere (0:59): a Go live review of five ready destinations
 *    starting, five live; destination cards (video "source, copied", speed,
 *    dropped and duplicated frames); the Routing page's output loudness
 *    "measured as it leaves" for each destination.
 *
 * @typedef {import("../lib/facts").Fact} Fact */

export const TOUR_ROUTE = "/tour";

/** @type {{ title: string, description: string, heading: string, accent: string, lede: string,
 *   filmedVersion: string, notes: { facts: Fact[], docs: string[] }[] }} */
export const TOUR_PAGE = {
  title: "Video tour of the polyemesis console",
  description:
    "Watch a match feed come in, split into a mix per destination and go live, with on-screen captions and no voiceover. Chapters and docs below.",
  heading: "A tour of the console.",
  accent: "tour",
  lede: "A match feed comes in, is split into a mix per destination and goes live, with captions on screen and no voiceover.",
  /** The release the demo rig was filmed on; the page states its date from CHANGELOG.md. */
  filmedVersion: "0.18.0",
  notes: [
    {
      facts: [
        {
          text: "The feed arrives as one SRT stream, which carries every audio track together.",
          doc: "obs",
          evidence: ["several audio tracks in one MPEG-TS stream over SRT"],
        },
        {
          text: "The dashboard shows the incoming stream as live.",
          doc: "quickstart",
          evidence: ["show the ingest live"],
          anchor: "4-point-obs-at-it",
        },
        {
          text: "Each incoming track gets its own meter: here five commentary languages and the crowd, six tracks in all.",
          doc: "quickstart",
          evidence: ["a meter per incoming track"],
          anchor: "4-point-obs-at-it",
        },
      ],
      docs: ["obs", "quickstart", "install"],
    },
    {
      facts: [
        {
          text: "The tracks are labelled once, for the feed, so every destination sees the same names.",
          doc: "quickstart",
          evidence: ["every destination sees the same set"],
          anchor: "5-tell-it-what-each-track-is",
        },
        {
          text: "Each destination ticks the tracks it should carry: a row per track, with a checkbox and a gain slider.",
          doc: "audio-routing",
          evidence: ["A row per ingest track with a checkbox and a gain slider"],
          anchor: "simple-mode",
        },
        {
          text: "Every destination gets its own mix: in the tour, one language each and the crowd on all five.",
          doc: "audio-routing",
          evidence: ["Every destination gets its own mix"],
        },
      ],
      docs: ["audio-routing", "quickstart"],
    },
    {
      facts: [
        {
          text: "Video is copied, not re-encoded: each destination's card reads source, copied for its video.",
          doc: "quickstart",
          evidence: ["Video is copied, not re-encoded"],
          anchor: "6-add-a-destination",
        },
        {
          text: "Each destination's card shows its own live numbers, including its speed and its dropped and duplicated frames.",
          doc: "monitoring",
          evidence: ["The same numbers are on each destination's card, live", "dropped frames", "duplicated frames"],
          anchor: "a-destination-falling-behind-realtime",
        },
        {
          text: "The loudness shown for a destination is measured after routing, which is what the platform on the other end receives.",
          doc: "quickstart",
          evidence: ["measured after routing", "what the platform on the other end receives"],
          anchor: "7-watch-that-it-is-actually-right",
        },
      ],
      docs: ["connecting-platforms", "platforms", "monitoring"],
    },
  ],
};
