/* The three audience pages (/for/...): their metadata, what each needs, the
 * steps to set it up and the docs worth reading next. Every product sentence is
 * a Fact (lib/facts.ts): the doc it comes from and the phrases that prove it,
 * checked against docs/ by audiences-data.test.ts. A page never types a track
 * or destination name; it writes {tracks}, {dests} and {trackCount}.
 */

/**
 * @typedef {import("../lib/facts").Fact} Fact
 * @typedef {import("../lib/facts").Step} Step
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

/** The picture's label on the pages that borrow the sports-club demo install. */
const DEMO_CAPTION =
  "Demo install: the destinations and tracks in this picture are the sports-club example's; yours are the ones you add and label.";

/** @type {Fact} */
const SIX_TRACKS = {
  text: "OBS sends up to six audio tracks.",
  doc: "audio-routing",
  evidence: ["six is what OBS sends"],
};
/** @type {Fact} */
const SRT_FACT = {
  text: "Multitrack SRT into polyemesis. Classic RTMP carries a single track.",
  doc: "obs",
  evidence: ["Multitrack over SRT", "Classic RTMP carries one audio track"],
};
/** @type {Fact} */
const ADVANCED_AUDIO = {
  text: "In OBS, open Advanced Audio Properties in the Audio Mixer and tick which tracks each source feeds.",
  doc: "obs",
  evidence: ["Advanced Audio Properties"],
  anchor: "multitrack-over-srt",
};
/** @type {Fact} */
const OUTPUT_TO_URL = {
  text: "Use the Recording output set to Output to URL with container mpegts, and press Start Recording.",
  doc: "obs",
  evidence: ["Output to URL", "mpegts", "Start Recording"],
  anchor: "multitrack-over-srt",
};
/** @type {Fact} */
const RTMP_SINGLE = {
  text: "Classic RTMP carries a single track, so multitrack audio needs SRT.",
  doc: "obs",
  evidence: ["Classic RTMP carries one audio track"],
  anchor: "standard-rtmp-single-track",
};
/** @type {Fact} */
const LABEL_TRACKS = {
  text: "On Sources → annotations, label the incoming tracks so every destination sees the same names.",
  doc: "quickstart",
  evidence: ["annotations"],
  anchor: "5-tell-it-what-each-track-is",
};
/** @type {Fact} */
const ADD_DESTINATION = {
  text: "Add one destination for each under Destinations → Add and tick the tracks it should carry.",
  doc: "quickstart",
  evidence: ["Destinations → Add", "tick the ones this destination should carry"],
  anchor: "6-add-a-destination",
};
/** @type {Fact} */
const METERS_AFTER_ROUTING = {
  text: "The Meters page shows loudness measured after routing, which is what each platform receives.",
  doc: "quickstart",
  evidence: ["measured after routing"],
  anchor: "7-watch-that-it-is-actually-right",
};
/** @type {Fact} */
const SILENCE_FACT = {
  text: "A destination set to a track the source is not sending gets silence.",
  doc: "quickstart",
  evidence: ["selecting a track the source is not sending"],
};
/** @type {Fact} */
const CONNECT_ACCOUNT = {
  text: "A connected account for each platform you send to.",
  doc: "connecting-platforms",
  evidence: ["Connect account"],
  anchor: "the-same-four-steps-on-every-platform",
};

/** @type {Step} */
const STEP_SEND_SRT = { title: "Send it as multitrack SRT", facts: [OUTPUT_TO_URL, RTMP_SINGLE] };
/** @type {Step} */
const STEP_LABEL = { title: "Label the tracks", facts: [LABEL_TRACKS] };
/** @type {Step} */
const STEP_CHECK = {
  title: "Check what each destination hears",
  facts: [METERS_AFTER_ROUTING, SILENCE_FACT],
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
      SRT_FACT,
      {
        text: "A connected YouTube account for each channel.",
        doc: "connecting-platforms",
        evidence: ["YouTube", "Connect account"],
        anchor: "the-same-four-steps-on-every-platform",
      },
    ],
    steps: [
      {
        title: "Put each of {tracks} on its own track",
        facts: [
          ADVANCED_AUDIO,
          { text: "OBS sends six tracks, so five languages plus the crowd fit.", doc: "audio-routing", evidence: ["six is what OBS sends"] },
        ],
      },
      STEP_SEND_SRT,
      STEP_LABEL,
      {
        title: "One destination per channel",
        facts: [
          {
            text: "Connect each YouTube channel: register an app, paste its credentials in Settings, press Connect account.",
            doc: "connecting-platforms",
            evidence: ["Register an app", "Connect account"],
            anchor: "the-same-four-steps-on-every-platform",
          },
          ADD_DESTINATION,
        ],
        shot: {
          file: "18-destinations.png",
          alt: "The Destinations page, one card per destination with the tracks it carries",
          chrome: "Destinations",
        },
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
    mixesAfterStep: 4,
    band: { heading: "Your match feed, in every language.", accent: "language", lede: "Run it on a server you control. Your keys and settings stay on it." },
  },

  worship: {
    key: "worship",
    route: AUDIENCE_ROUTES.worship,
    title: "One service, a mix for every room — polyemesis",
    description:
      "Give the room, the online congregation and a second-language channel the version made for them, and send a speech-only feed out as a podcast.",
    heading: "One service, a mix for every room.",
    accent: "mix",
    lede: "The room, the online congregation and a second-language channel each hear the version made for them, while a speech-only feed goes out as a podcast.",
    crumbLabel: "for churches and events",
    needs: [
      { text: "One audio track for each of {tracks}. OBS sends up to six.", doc: "audio-routing", evidence: ["six is what OBS sends"] },
      SRT_FACT,
      CONNECT_ACCOUNT,
    ],
    steps: [
      { title: "Put each of {tracks} on its own track", facts: [ADVANCED_AUDIO] },
      STEP_SEND_SRT,
      STEP_LABEL,
      {
        title: "Tick the tracks each destination carries",
        facts: [ADD_DESTINATION],
        shot: {
          file: "18-destinations.png",
          alt: "The Destinations page, one card per destination with the tracks it carries",
          chrome: "Destinations",
          caption: DEMO_CAPTION,
        },
      },
      STEP_CHECK,
    ],
    features: ["captions", "scheduled-broadcast", "recurring-events", "storage", "rollover"],
    related: ["two-languages-one-broadcast"],
    mixesAfterStep: 4,
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
      SRT_FACT,
      {
        text: "A rendition for the vertical output: it re-encodes video only, and audio passes through untouched.",
        doc: "renditions",
        evidence: ["A rendition re-encodes video only"],
        anchor: "a-rendition-re-encodes-video-only",
      },
    ],
    steps: [
      { title: "Put each of {tracks} on its own track", facts: [ADVANCED_AUDIO] },
      STEP_SEND_SRT,
      STEP_LABEL,
      {
        title: "Add a 9:16 rendition",
        facts: [
          {
            text: "A 9:16 rendition of a 16:9 ingest is one more entry in the ladder, encoded once and shared.",
            doc: "renditions",
            evidence: ["one more entry in the ladder", "9:16 rendition"],
            anchor: "aspect-handling--one-ladder-rung-not-a-second-pipeline",
          },
          {
            text: "Blurred fill fills the remainder with a blurred copy of the frame itself.",
            doc: "renditions",
            evidence: ["Blurred fill"],
            anchor: "aspect-handling--one-ladder-rung-not-a-second-pipeline",
          },
        ],
        shot: {
          file: "06-renditions.png",
          alt: "The Renditions page, one card per rendition with the destinations it feeds",
          chrome: "Renditions",
          caption: "Demo install: the renditions and destinations in this picture are examples; yours are the ones you add.",
        },
      },
      {
        title: "Point each destination at its own video",
        facts: [
          {
            text: "Point the vertical destination at the rendition, leave the others on the copied video, and tick each one's tracks.",
            doc: "quickstart",
            evidence: ["tick the ones this destination should carry"],
            anchor: "6-add-a-destination",
          },
        ],
        shot: {
          file: "18-destinations.png",
          alt: "The Destinations page, one card per destination showing its video and the tracks it carries",
          chrome: "Destinations",
          caption: DEMO_CAPTION,
        },
      },
      STEP_CHECK,
    ],
    features: ["renditions", "automod", "overlay", "clip-publishing", "stream-deck"],
    related: ["streaming-from-a-backpack", "obs-to-five-platforms"],
    mixesAfterStep: 0,
    band: { heading: "Every output, its own mix.", accent: "mix", lede: "Run it on a server you control. Your keys and settings stay on it." },
  },
};
