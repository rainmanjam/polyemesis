/* The three audience pages (/for/...): their metadata, what each needs, the
 * steps to set it up and the docs worth reading next. Every product sentence is
 * a Fact (lib/facts.ts): the doc it comes from and the phrases that tie it to
 * that doc, checked against docs/ by audiences-data.test.ts. A page never types
 * a track or destination name; it writes {tracks}, {dests} and {trackCount}.
 *
 * A page is complete (ruling P4-R17): a reader following only its steps, from a
 * fresh working install, ends with what its heading promises. The steps the
 * pages share with the recipes are the Facts in setup-facts.mjs, defined once;
 * the completeness guards in audiences-data.test.ts pin the gaps a review found.
 */

import * as SETUP from "./setup-facts.mjs";

/**
 * @typedef {import("../lib/facts").Fact} Fact
 * @typedef {import("../lib/facts").Step} Step
 * @typedef {import("../lib/facts").StepShot} StepShot
 * @typedef {"sports"|"worship"|"streamer"} CaseKey
 * @typedef {{ key: CaseKey, route: string, title: string, description: string, heading: string, accent: string,
 *   lede: string, crumbLabel: string, needs: Fact[], steps: Step[], features: string[], related: string[],
 *   band: { heading: string, accent: string, lede: string },
 *   mixesAfterStep: number }} Audience */

export const AUDIENCE_ROUTES = {
  sports: "/for/sports-clubs",
  worship: "/for/churches-and-events",
  streamer: "/for/streamers",
};

/** @type {Step} */
const STEP_TRACKS = { title: "Put each of {tracks} on its own track", facts: [SETUP.OBS_BITRATES, SETUP.OBS_TRACK_TICKS] };
/** Every action from "nothing to push to" to "OBS is pushing", in the docs' order.
 *  @type {Step} */
const STEP_SEND_SRT = {
  title: "Send it as multitrack SRT",
  facts: [
    SETUP.ADD_SOURCE,
    SETUP.COPY_URL,
    SETUP.OBS_CUSTOM_OUTPUT,
    SETUP.PASTE_URL,
    SETUP.AUDIO_TRACKS,
    SETUP.ENCODER_SETTINGS,
    SETUP.START_RECORDING,
    SETUP.METERS_APPEAR,
  ],
};
/** The streamer's destinations may re-encode through a rendition, so "your encoder settings are what every destination
 *  receives" is not said there. */
/** @type {Step} */
const STEP_SEND_SRT_STREAMER = { ...STEP_SEND_SRT, facts: STEP_SEND_SRT.facts.filter((f) => f !== SETUP.ENCODER_SETTINGS) };
/** The church's example source is a hardware encoder, which the docs cover only as "anything that can push MPEG-TS over SRT".
 *  @type {Step} */
const STEP_SEND_SRT_WORSHIP = { ...STEP_SEND_SRT, facts: [SETUP.ANY_SRT_ENCODER, ...STEP_SEND_SRT.facts] };
/** @type {Step} */
const STEP_LABEL = { title: "Label the tracks (optional)", facts: [SETUP.LABEL_TRACKS] };
/** The key route needs nothing from this step; the account route needs all of it, and before the destination is added.
 *  @type {Step} */
const STEP_CONNECT = {
  title: "Connect an account (optional)",
  facts: [
    SETUP.CONNECT_NEEDS_HOSTNAME,
    SETUP.APP_REQUIREMENTS,
    SETUP.REDIRECT_URL,
    SETUP.REGISTER_APP,
    SETUP.PASTE_CREDENTIALS,
    SETUP.SAME_FOUR_STEPS,
  ],
};
/** @type {Step} */
const STEP_CHECK = {
  title: "Check what each destination hears",
  facts: [SETUP.METERS_AFTER_ROUTING, SETUP.SILENCE_FACT],
};

/** Where the sports page puts its two pictures: the destinations step and the check. */
/** @type {StepShot} */
const SPORTS_DESTINATIONS_SHOT = {
  file: "18-destinations.png",
  alt: "The dashboard's Destinations section, one card per destination with the tracks it carries",
  chrome: "Dashboard",
  caption: "Demo install: the five YouTube channels and their tracks in this picture are example names; yours are the ones you add and label.",
};

/** @type {Record<CaseKey, Audience>} */
export const AUDIENCES = {
  sports: {
    key: "sports",
    route: AUDIENCE_ROUTES.sports,
    title: "Five commentary languages from one match feed — polyemesis",
    description:
      "Send one match feed to five YouTube channels, each with its own commentary language over the same crowd sound. Video is copied, not re-encoded.",
    heading: "One match feed, five languages.",
    accent: "languages",
    lede: "One match picture goes to five YouTube channels, and each channel carries its own commentary language over the same crowd sound.",
    crumbLabel: "for sports clubs",
    needs: [
      {
        text: "One audio track per commentary language, plus one for the crowd. OBS sends up to six.",
        doc: "audio-routing",
        evidence: ["six is what OBS sends"],
      },
      SETUP.SRT_NEED,
      SETUP.KEY_OR_ACCOUNT_NEED,
    ],
    steps: [
      {
        ...STEP_TRACKS,
        facts: [
          ...STEP_TRACKS.facts,
          { text: "OBS sends six tracks, so five languages plus the crowd fit.", doc: "audio-routing", evidence: ["six is what OBS sends"] },
        ],
      },
      STEP_SEND_SRT,
      STEP_LABEL,
      STEP_CONNECT,
      {
        title: "One destination per channel",
        facts: [SETUP.ADD_DESTINATION, SETUP.URL_AND_KEY, SETUP.TICK_TRACKS, SETUP.ENABLE_DESTINATION, SETUP.COPY_COSTS],
        shot: SPORTS_DESTINATIONS_SHOT,
      },
      {
        ...STEP_CHECK,
        shot: {
          file: "04-meters.png",
          alt: "The Meters page, loudness per destination after routing",
          chrome: "Audio meters",
        },
      },
    ],
    features: ["link-health", "broadcast-delay", "rollover", "monitoring", "stream-deck"],
    related: ["two-languages-one-broadcast"],
    mixesAfterStep: 5,
    band: { heading: "Your match feed, in every language.", accent: "language", lede: "Run it on a server you control. Your keys and settings stay on it." },
  },

  worship: {
    key: "worship",
    route: AUDIENCE_ROUTES.worship,
    title: "One service, a mix for every room — polyemesis",
    description:
      "Send one service to several destinations, each with the mix made for it: the room mix, the broadcast mix, a second language or music only.",
    heading: "One service, a mix for every room.",
    accent: "mix",
    lede: "One service goes out to several destinations, and each one carries the mix made for it: the room mix, the broadcast mix, a second language, or music only.",
    crumbLabel: "for churches and events",
    needs: [
      { text: "One audio track for each of {tracks}. OBS sends up to six.", doc: "audio-routing", evidence: ["six is what OBS sends"] },
      SETUP.SRT_NEED,
      SETUP.KEY_OR_ACCOUNT_NEED,
    ],
    steps: [
      STEP_TRACKS,
      STEP_SEND_SRT_WORSHIP,
      STEP_LABEL,
      STEP_CONNECT,
      {
        title: "Add a destination for each mix",
        facts: [SETUP.ADD_DESTINATION, SETUP.URL_AND_KEY, SETUP.TICK_TRACKS, SETUP.ENABLE_DESTINATION, SETUP.COPY_COSTS],
        shot: { ...SETUP.DESTINATIONS_SHOT, alt: "The dashboard's Destinations section, one card per destination with the tracks it carries" },
      },
      STEP_CHECK,
    ],
    features: ["captions", "scheduled-broadcast", "recurring-events", "storage", "rollover"],
    related: ["two-languages-one-broadcast"],
    mixesAfterStep: 5,
    band: { heading: "Every room hears its own mix.", accent: "mix", lede: "Run it on a server you control. Your keys and settings stay on it." },
  },

  streamer: {
    key: "streamer",
    route: AUDIENCE_ROUTES.streamer,
    title: "Wide and vertical, each with its own mix — polyemesis",
    description:
      "Send your 16:9 stream as it is, add a 9:16 version as one shared encode, and give each output the audio mix that suits its audience.",
    heading: "Wide and vertical, each with its own mix.",
    accent: "mix",
    lede: "Your 16:9 stream goes out as it is. A 9:16 version is one shared encode, and each output keeps the audio mix that suits its audience.",
    crumbLabel: "for streamers",
    needs: [
      { text: "One audio track for each of {tracks}. OBS sends up to six.", doc: "audio-routing", evidence: ["six is what OBS sends"] },
      SETUP.SRT_NEED,
      SETUP.KEY_OR_ACCOUNT_NEED,
      {
        text: "A rendition for the vertical output: it re-encodes video only, and audio passes through untouched.",
        doc: "renditions",
        evidence: ["A rendition re-encodes video only"],
        anchor: "a-rendition-re-encodes-video-only",
      },
    ],
    steps: [
      STEP_TRACKS,
      STEP_SEND_SRT_STREAMER,
      STEP_LABEL,
      STEP_CONNECT,
      {
        title: "Add a destination for each output",
        facts: [SETUP.ADD_DESTINATION, SETUP.URL_AND_KEY, SETUP.TICK_TRACKS],
        shot: { ...SETUP.DESTINATIONS_SHOT, alt: "The dashboard's Destinations section, one card per destination with the tracks it carries" },
      },
      {
        title: "Add a 9:16 rendition",
        facts: [
          {
            text: "Different resolutions per destination are set up on the Renditions page.",
            doc: "quickstart",
            evidence: ["Different resolutions per destination", "Renditions"],
            anchor: "what-to-do-next",
          },
          {
            text: "The rendition editor offers presets as editable starting points; give the rendition both a width and a height in a 9:16 shape, because aspect handling needs both.",
            doc: "renditions",
            evidence: ["The rendition editor offers these as editable starting points", "Aspect handling needs both a width and a height"],
          },
          {
            text: "A 9:16 rendition of a 16:9 ingest is one more entry in the ladder, encoded once and shared.",
            doc: "renditions",
            evidence: ["one more entry in the ladder", "9:16 rendition"],
            anchor: "aspect-handling--one-ladder-rung-not-a-second-pipeline",
          },
          {
            text: "Choose Blurred fill, which fills the remainder with a blurred copy of the frame itself. The default, Stretch to fit, lets the picture distort.",
            doc: "renditions",
            evidence: ["Blurred fill", "lets the picture distort", "so it stays the default"],
            anchor: "aspect-handling--one-ladder-rung-not-a-second-pipeline",
          },
        ],
      },
      {
        title: "Point each destination at its own video",
        facts: [
          {
            text: "Destinations select a rendition rather than owning one, so point the vertical destination at the 9:16 rendition.",
            doc: "renditions",
            evidence: ["Destinations select a rendition rather than owning one"],
            anchor: "the-problem",
          },
          {
            text: "Every other destination stays on passthrough, which copies the source video, until you point it at a rendition.",
            doc: "renditions",
            evidence: ["Every destination stays on passthrough", "copies the source video"],
            anchor: "passthrough-is-a-rendition",
          },
          {
            text: "Each destination's card on the dashboard shows the rendition it is on, directly above the audio tracks it receives.",
            doc: "renditions",
            evidence: ["shows the rendition it is on directly above the audio tracks it receives"],
            anchor: "a-rendition-only-runs-when-something-needs-it",
          },
          SETUP.ENABLE_DESTINATION,
        ],
      },
      STEP_CHECK,
    ],
    features: ["renditions", "automod", "overlay", "clip-publishing", "stream-deck"],
    related: ["streaming-from-a-backpack", "obs-to-five-platforms"],
    mixesAfterStep: 0,
    band: { heading: "Every output, its own mix.", accent: "mix", lede: "Run it on a server you control. Your keys and settings stay on it." },
  },
};
