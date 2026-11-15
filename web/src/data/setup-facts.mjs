/* The setup facts the audience pages (audiences.mjs) and the recipes
 * (recipes.mjs) share, each defined once. A sentence about an action lives here
 * and nowhere else, so two pages cannot word the same step two ways and one
 * cannot drift behind the other. A Fact (lib/facts.ts) names the doc it comes
 * from and the phrases that tie it to that doc; setup facts are checked against
 * docs/ by recipes-data.test.ts and audiences-data.test.ts. The check proves a
 * phrase sits in the doc's section, not that the sentence is true: the sentence
 * is read against that paragraph by a person.
 *
 * Order inside a group is the order the docs give the steps in. No fact here
 * prints a token: the publish URL is "copy it from Sources", never a value. */

/**
 * @typedef {import("../lib/facts").Fact} Fact
 * @typedef {import("../lib/facts").StepShot} StepShot */

/** The picture's label where it borrows the sports-club demo install. */
export const DEMO_CAPTION =
  "Demo install: the destinations and tracks in this picture are the sports-club example's; yours are the ones you add and label.";

/** The picture is the dashboard's Destinations section, not the Destinations page.
 *  @type {Pick<StepShot, "file"|"chrome"|"caption">} */
export const DESTINATIONS_SHOT = {
  file: "18-destinations.png",
  chrome: "Dashboard",
  caption: DEMO_CAPTION,
};

/* ---- In OBS: the tracks (docs/OBS.md, "Multitrack over SRT", steps 1 and 2) ---- */

/** @type {Fact} */
export const OBS_BITRATES = {
  text: "In OBS's Advanced output mode, open Settings → Output → Audio and set a bitrate for each track you intend to use.",
  doc: "obs",
  evidence: ["Settings → Output → Audio", "Advanced mode", "Set a bitrate for each track you intend to use"],
  anchor: "1-enable-the-audio-tracks",
};
/** @type {Fact} */
export const OBS_TRACK_TICKS = {
  text: "In the Audio Mixer, open Advanced Audio Properties from the gear icon and tick which of the six Tracks checkboxes each source feeds.",
  doc: "obs",
  evidence: ["click the gear icon → Advanced Audio Properties", "Each source has six Tracks checkboxes"],
  anchor: "2-assign-sources-to-tracks",
};

/* ---- Sending it: the source, its URL, the OBS output (QUICKSTART 3 and 4, OBS.md 3 and 4) ---- */

/** @type {Fact} */
export const ADD_SOURCE = {
  text: "On the Sources page, add a source; it starts on SRT, the protocol for multitrack. Check its protocol on the card.",
  doc: "quickstart",
  evidence: ["Open the Sources page and add one", "A new source starts on SRT", "Check the protocol on the card"],
  anchor: "3-create-a-source",
};
/** @type {Fact} */
export const COPY_URL = {
  text: "Copy its publish URL from the Sources page. The URL's streamid is that source's publish token, which tells polyemesis which source you are.",
  doc: "quickstart",
  evidence: [
    "shows the publish URL for the source you just created",
    "that source's publish token",
    "tells polyemesis which source you are",
  ],
  anchor: "4-point-obs-at-it",
};
/** @type {Fact} */
export const OBS_CUSTOM_OUTPUT = {
  text: "In OBS, open Settings → Output → Output Mode: Advanced → Recording, set Type to Custom Output (FFmpeg) and FFmpeg Output Type to Output to URL.",
  doc: "obs",
  evidence: ["Output Mode: Advanced → Recording", "Custom Output (FFmpeg)", "Output to URL"],
  anchor: "3-configure-the-custom-ffmpeg-output",
};
/** @type {Fact} */
export const PASTE_URL = {
  text: "Paste the URL as the File path or URL and set Container Format to mpegts.",
  doc: "obs",
  evidence: ["File path or URL", "mpegts"],
  anchor: "3-configure-the-custom-ffmpeg-output",
};
/** @type {Fact} */
export const AUDIO_TRACKS = {
  text: "Tick the tracks you want under Audio Track in that same panel: the ones your sources are assigned to in Advanced Audio Properties, tracks 1 to 6.",
  doc: "quickstart",
  evidence: ["Tick the tracks you want under Audio Track", "the sources are assigned to tracks 1–6 in the Audio Mixer's Advanced Audio Properties"],
  anchor: "4-point-obs-at-it",
};
/** @type {Fact} */
export const ENCODER_SETTINGS = {
  text: "Set the encoder you normally use: video is passed through untouched, so your encoder settings are what every destination receives.",
  doc: "quickstart",
  evidence: [
    "Set the encoder you normally use",
    "video is passed through untouched",
    "your encoder settings are what every destination receives",
  ],
  anchor: "4-point-obs-at-it",
};
/** @type {Fact} */
export const START_RECORDING = {
  text: "Press Start Recording, not Start Streaming: with Output to URL, that button is the SRT push.",
  doc: "obs",
  evidence: ["Start Recording, not Start Streaming", "Output to URL"],
  anchor: "4-start-it",
};
/** @type {Fact} */
export const METERS_APPEAR = {
  text: "The dashboard shows a meter for every incoming track within a couple of seconds.",
  doc: "quickstart",
  evidence: ["couple of seconds"],
  anchor: "4-point-obs-at-it",
};
/** @type {Fact} */
export const SRT_NEED = {
  text: "SRT in: Classic RTMP carries a single track, so if your encoder is OBS, use SRT.",
  doc: "obs",
  evidence: ["Classic RTMP carries one audio track", "If your encoder is OBS, use SRT"],
};

/** @type {Fact} */
export const ANY_SRT_ENCODER = {
  text: "polyemesis accepts a stream from anything that can push MPEG-TS over SRT. OBS is what most people use, so the steps here are OBS's.",
  doc: "obs",
  evidence: ["anything that can push MPEG-TS over SRT", "OBS is what most people use"],
};

/* ---- Labelling the tracks (QUICKSTART 5) ---- */

/** @type {Fact} */
export const LABEL_TRACKS = {
  text: "On Sources → annotations, label the incoming tracks with names such as mic, commentary or a language where it matters. It is optional; the labels belong to the feed, so every destination sees the same set.",
  doc: "quickstart",
  evidence: ["annotations", "a language where it matters", "This is optional", "every destination sees the same set"],
  anchor: "5-tell-it-what-each-track-is",
};

/* ---- Connecting an account (docs/CONNECTING-PLATFORMS.md) ---- */

/** @type {Fact} */
export const CONNECT_NEEDS_HOSTNAME = {
  text: "Connecting an account needs a real hostname with a trusted certificate, and the dashboard opened by that hostname, never its IP.",
  doc: "connecting-platforms",
  evidence: ["Use a real hostname with a trusted certificate", "Open the dashboard by its hostname, never its IP"],
  anchor: "server-prerequisites",
};
/** @type {Fact} */
export const APP_REQUIREMENTS = {
  text: "Each platform's app has its own requirements, such as the scopes it must allow, a client type or a test user. The connecting guide lists them under each platform's heading: X, Twitch, Kick, YouTube and Facebook.",
  doc: "connecting-platforms",
  evidence: [
    "so the app must allow them",
    "client type Confidential",
    "Request the scopes polyemesis uses",
    "add your Google account as a test user",
  ],
};
/** @type {Fact} */
export const REDIRECT_URL = {
  text: "Every platform's redirect URL has the same form, https://your-hostname/api/v1/oauth/<platform>/callback, and Settings → Platform credentials shows the exact one for each platform, with a copy button.",
  doc: "connecting-platforms",
  evidence: [
    "https://polyemesis.example.com/api/v1/oauth/<platform>/callback",
    "shows the exact string for each platform",
    "with a copy button",
  ],
  anchor: "server-prerequisites",
};
/** @type {Fact} */
export const REGISTER_APP = {
  text: "Register an app in the platform's developer console, with that redirect URL.",
  doc: "connecting-platforms",
  evidence: ["Register an app", "with your redirect URL"],
  anchor: "the-same-four-steps-on-every-platform",
};
/** @type {Fact} */
export const PASTE_CREDENTIALS = {
  text: "Paste the app's credentials, its client ID and secret (App ID and App secret on Facebook), into Settings → Platform credentials, press Connect account and approve the sign-in.",
  doc: "connecting-platforms",
  evidence: [
    "Paste its credentials into Settings → Platform credentials",
    "client ID and secret",
    "App ID and App secret on Facebook",
    "Connect account",
    "approve the sign-in",
  ],
  anchor: "the-same-four-steps-on-every-platform",
};
/** The doc's own words on Facebook: its setup is still being verified and Meta's review gates other people's accounts.
 *  @type {Fact} */
export const SAME_FOUR_STEPS = {
  text: "The same steps apply to X, Twitch, Kick, YouTube and Facebook. Facebook is still being verified: Meta requires App Review before anyone but you can connect an account.",
  doc: "connecting-platforms",
  evidence: [
    "The same four steps on every platform",
    "This setup is also being verified on a live server",
    "requires App Review before anyone but you can connect an account",
  ],
};

/* ---- Adding a destination (QUICKSTART 6) ---- */

/** What a page that sends to platforms needs first: a key from each platform's dashboard, or a connected account.
 *  The step's own sentence is URL_AND_KEY; this is the need that comes before it.
 *  @type {Fact} */
export const KEY_OR_ACCOUNT_NEED = {
  text: "For each platform you send to, its stream key from the platform's dashboard, or a connected account that lets polyemesis fetch it.",
  doc: "quickstart",
  evidence: ["platform's own dashboard", "Connected account", "have polyemesis fetch them"],
  anchor: "6-add-a-destination",
};

/** @type {Fact} */
export const ADD_DESTINATION = {
  text: "Under Destinations → Add, add each destination and pick its platform; the form takes on that platform's limits.",
  doc: "quickstart",
  evidence: ["Destinations → Add", "pick one, and the form takes on that platform's limits"],
  anchor: "6-add-a-destination",
};
/** The account route's own step, after the account is connected: where it is chosen on a destination.
 *  @type {Fact} */
export const PICK_ACCOUNT = {
  text: "Under Connected account, pick the account you connected for that platform.",
  doc: "connecting-platforms",
  evidence: ["Destinations → Add", "pick the connected account under Connected account"],
  anchor: "the-same-four-steps-on-every-platform",
};
/** A destination's key comes from the platform's dashboard or from a connected account: either works.
 *  @type {Fact} */
export const URL_AND_KEY = {
  text: "Give each destination the platform's URL and stream key from the platform's own dashboard, or leave them empty and pick a connected account to have polyemesis fetch them.",
  doc: "quickstart",
  evidence: ["from the platform's own dashboard", "leave them empty and pick a Connected account", "to have polyemesis fetch them"],
  anchor: "6-add-a-destination",
};
/** @type {Fact} */
export const TICK_TRACKS = {
  text: "Under Tracks, tick the ones each destination should carry.",
  doc: "quickstart",
  evidence: ["tick the ones this destination should carry"],
  anchor: "6-add-a-destination",
};
/** @type {Fact} */
export const ENABLE_DESTINATION = {
  text: "Start each destination with the Start button on its card.",
  doc: "quickstart",
  evidence: ["Start it with the Start button on its card", "That is what enabling a destination means"],
  anchor: "6-add-a-destination",
};
/** Said where the video is passed through, never where a rendition re-encodes it.
 *  @type {Fact} */
export const COPY_COSTS = {
  text: "Video is copied, not re-encoded, so each destination costs almost nothing.",
  doc: "quickstart",
  evidence: ["Video is copied, not re-encoded", "costs almost nothing"],
  anchor: "6-add-a-destination",
};

/* ---- Checking it (QUICKSTART 7, "Common first-run problems") ---- */

/** @type {Fact} */
export const METERS_AFTER_ROUTING = {
  text: "The Meters page shows loudness measured after routing, which is what the platform on the other end receives.",
  doc: "quickstart",
  evidence: ["measured after routing", "what the platform on the other end receives"],
  anchor: "7-watch-that-it-is-actually-right",
};
/** @type {Fact} */
export const SILENCE_FACT = {
  text: "A destination set to a track the source is not sending gets silence.",
  doc: "quickstart",
  evidence: ["selecting a track the source is not sending"],
};
