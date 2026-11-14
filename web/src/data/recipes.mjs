/* The three recipe pages (/recipes/...): their metadata, what each needs, the
 * four steps, how to check it worked and the docs worth reading next. Every
 * product sentence is a Fact (lib/facts.ts): the doc it comes from and the
 * phrases that prove it, checked against docs/ by recipes-data.test.ts.
 * Step titles are the homepage's (components/home/Use.astro) without the final
 * period; the homepage test keeps the two in step.
 *
 * A recipe is complete (ruling P4-R12): inside its four steps it carries every
 * action the docs require to get from the step before to the step after, so a
 * reader following only the page reaches what the title says. The completeness
 * guards in recipes-data.test.ts pin the gaps a review found.
 */

/**
 * @typedef {import("../lib/facts").Fact} Fact
 * @typedef {import("../lib/howto").Recipe} Recipe */

/** The picture's label where it borrows the sports-club demo install. */
const DEMO_CAPTION =
  "Demo install: the destinations and tracks in this picture are an example; yours are the ones you add and label.";

/** The picture is the dashboard's Destinations section, not the Destinations page. */
const DESTINATIONS_SHOT = {
  file: "18-destinations.png",
  chrome: "Dashboard",
  caption: DEMO_CAPTION,
};

/* The ingest steps, from OBS.md and QUICKSTART.md in the order those docs give them. */
/** @type {Fact} */
const ADD_SOURCE = {
  text: "On the Sources page, add a source, pick SRT as its protocol and press Apply.",
  doc: "quickstart",
  evidence: ["Open the Sources page and add one", "press Apply"],
  anchor: "3-create-a-source",
};
/** @type {Fact} */
const COPY_URL = {
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
const OBS_CUSTOM_OUTPUT = {
  text: "In OBS, open Settings → Output → Output Mode: Advanced → Recording, set Type to Custom Output (FFmpeg) and FFmpeg Output Type to Output to URL.",
  doc: "obs",
  evidence: ["Output Mode: Advanced → Recording", "Custom Output (FFmpeg)", "Output to URL"],
  anchor: "3-configure-the-custom-ffmpeg-output",
};
/** @type {Fact} */
const PASTE_URL = {
  text: "Paste the URL as the File path or URL and set Container Format to mpegts.",
  doc: "obs",
  evidence: ["File path or URL", "mpegts"],
  anchor: "3-configure-the-custom-ffmpeg-output",
};
/** @type {Fact} */
const AUDIO_TRACKS = {
  text: "Tick the tracks you want under Audio Track in that same panel.",
  doc: "quickstart",
  evidence: ["Tick the tracks you want under Audio Track"],
  anchor: "4-point-obs-at-it",
};
/** @type {Fact} */
const START_RECORDING = {
  text: "Press Start Recording, not Start Streaming: with Output to URL, that button is the SRT push.",
  doc: "obs",
  evidence: ["Start Recording, not Start Streaming", "Output to URL"],
  anchor: "4-start-it",
};
/** @type {Fact} */
const METERS_AFTER_ROUTING = {
  text: "The Meters page shows loudness measured after routing.",
  doc: "quickstart",
  evidence: ["measured after routing"],
  anchor: "7-watch-that-it-is-actually-right",
};
/** @type {Fact} */
const ENABLE_IT = {
  text: "Tick the tracks it should carry, then enable it.",
  doc: "quickstart",
  evidence: ["tick the ones this destination should carry", "Enable it"],
  anchor: "6-add-a-destination",
};

/** @type {Recipe[]} */
export const RECIPES = [
  {
    slug: "two-languages-one-broadcast",
    route: "/recipes/two-languages-one-broadcast",
    title: "Two languages, one broadcast — polyemesis recipe",
    description:
      "Give each voice its own OBS track, send them as one SRT stream, and give each destination the language it needs. Four steps from a working install.",
    heading: "Two languages, one broadcast.",
    accent: "one",
    lede: "Each voice gets its own track, and each destination gets the language it needs, from one upload.",
    tag: "audio routing · OBS",
    needs: [
      { text: "Up to six tracks from OBS.", doc: "audio-routing", evidence: ["six is what OBS sends"] },
      {
        text: "SRT in: Classic RTMP carries a single track.",
        doc: "obs",
        evidence: ["Classic RTMP carries one audio track"],
      },
    ],
    steps: [
      {
        title: "Give each voice its own track",
        facts: [
          {
            text: "In OBS's Advanced output mode, open Settings → Output → Audio and set a bitrate for each track you intend to use.",
            doc: "obs",
            evidence: ["Settings → Output → Audio", "Advanced mode", "Set a bitrate for each track you intend to use"],
            anchor: "1-enable-the-audio-tracks",
          },
          {
            text: "Then open Advanced Audio Properties and tick which of the six tracks each source feeds.",
            doc: "obs",
            evidence: ["Advanced Audio Properties", "six"],
            anchor: "2-assign-sources-to-tracks",
          },
        ],
      },
      {
        title: "Send it as one SRT stream",
        facts: [
          ADD_SOURCE,
          COPY_URL,
          OBS_CUSTOM_OUTPUT,
          PASTE_URL,
          AUDIO_TRACKS,
          START_RECORDING,
          {
            text: "The dashboard shows a meter for every incoming track within a couple of seconds.",
            doc: "quickstart",
            evidence: ["couple of seconds"],
          },
        ],
      },
      {
        title: "Label the tracks",
        facts: [
          {
            text: "On Sources → annotations, name them: mic, commentary, and a language where it matters.",
            doc: "quickstart",
            evidence: ["annotations", "a language where it matters"],
            anchor: "5-tell-it-what-each-track-is",
          },
        ],
      },
      {
        title: "One destination per language",
        facts: [
          {
            text: "Under Destinations → Add, make one destination for each language.",
            doc: "quickstart",
            evidence: ["Destinations → Add"],
            anchor: "6-add-a-destination",
          },
          {
            text: "Give each the platform's URL and stream key from the platform's own dashboard, or pick a connected account to have polyemesis fetch them.",
            doc: "quickstart",
            evidence: ["from the platform's own dashboard", "pick a Connected account"],
            anchor: "6-add-a-destination",
          },
          ENABLE_IT,
          {
            text: "Then check the Meters page, which measures after routing.",
            doc: "quickstart",
            evidence: ["measured after routing"],
            anchor: "7-watch-that-it-is-actually-right",
          },
        ],
        shot: {
          ...DESTINATIONS_SHOT,
          alt: "The dashboard's Destinations section, one card per destination showing the tracks it carries",
        },
      },
    ],
    check: [
      {
        text: "Play music in OBS and open Audio meters: the music track moves while the clean track stays flat.",
        doc: "obs",
        evidence: ["play music", "track 1 moves while track 2 stays flat"],
        anchor: "5-verify",
      },
      { ...METERS_AFTER_ROUTING },
      {
        text: "A destination set to a track the source is not sending gets silence.",
        doc: "quickstart",
        evidence: ["selecting a track the source is not sending"],
      },
      {
        text: "The audio routing guide has a section on verifying a mix before you go live.",
        doc: "audio-routing",
        evidence: ["Verifying it before you go live"],
        anchor: "verifying-it-before-you-go-live",
      },
    ],
    docs: ["audio-routing", "obs", "quickstart"],
  },
  {
    slug: "obs-to-five-platforms",
    route: "/recipes/obs-to-five-platforms",
    title: "OBS to five platforms from one server — polyemesis recipe",
    description:
      "Send OBS to your own polyemesis server once, connect each platform's account, and add a destination for each. Four steps from a working install.",
    heading: "OBS to five platforms.",
    accent: "platforms",
    lede: "One connection from OBS to your server, and a destination for each platform.",
    tag: "OBS · connecting platforms",
    needs: [
      {
        text: "A real hostname with a trusted certificate, and the dashboard opened by that hostname, never its IP.",
        doc: "connecting-platforms",
        evidence: ["Use a real hostname with a trusted certificate", "Open the dashboard by its hostname, never its IP"],
        anchor: "server-prerequisites",
      },
      {
        text: "A developer app on each platform you connect.",
        doc: "connecting-platforms",
        evidence: ["Register an app"],
      },
    ],
    steps: [
      {
        title: "Run polyemesis and make your admin",
        facts: [
          {
            text: "A fresh install asks you to set an admin password, with the one-time setup code the server saved.",
            doc: "quickstart",
            evidence: ["Set an admin password", "setup-code"],
            anchor: "2-set-an-admin-password",
          },
        ],
      },
      {
        title: "Point OBS at it",
        facts: [ADD_SOURCE, COPY_URL, OBS_CUSTOM_OUTPUT, PASTE_URL, START_RECORDING],
      },
      {
        title: "Connect each platform",
        facts: [
          {
            text: "Each platform's app has its own requirements, such as the scopes it must allow, a client type or a test user. The connecting guide lists them under each platform's heading: X, Twitch, Kick, YouTube and Facebook.",
            doc: "connecting-platforms",
            evidence: [
              "so the app must allow them",
              "client type Confidential",
              "Request the scopes polyemesis uses",
              "add your Google account as a test user",
            ],
          },
          {
            text: "Every platform's redirect URL has the same form, https://your-hostname/api/v1/oauth/<platform>/callback, and Settings → Platform credentials shows the exact one for each platform, with a copy button.",
            doc: "connecting-platforms",
            evidence: [
              "https://polyemesis.example.com/api/v1/oauth/<platform>/callback",
              "shows the exact string for each platform",
              "with a copy button",
            ],
            anchor: "server-prerequisites",
          },
          {
            text: "Register an app in the platform's developer console, with that redirect URL.",
            doc: "connecting-platforms",
            evidence: ["Register an app", "with your redirect URL"],
            anchor: "the-same-four-steps-on-every-platform",
          },
          {
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
          },
          {
            text: "The same sign-in steps work for X, Twitch, Kick, YouTube and Facebook; Facebook is still being verified.",
            doc: "connecting-platforms",
            evidence: ["The same four steps on every platform", "Facebook (being verified)"],
          },
        ],
      },
      {
        title: "Add a destination per platform",
        facts: [
          {
            text: "Under Destinations → Add, choose the platform and pick the connected account under Connected account.",
            doc: "connecting-platforms",
            evidence: ["Destinations → Add", "pick the connected account under Connected account"],
            anchor: "the-same-four-steps-on-every-platform",
          },
          {
            text: "Leave the URL and stream key empty, and polyemesis fetches them from that account.",
            doc: "quickstart",
            evidence: ["leave them empty", "to have polyemesis fetch them"],
            anchor: "6-add-a-destination",
          },
          ENABLE_IT,
        ],
        shot: {
          ...DESTINATIONS_SHOT,
          alt: "The dashboard's Destinations section: five YouTube destinations, each live",
          caption:
            "Demo install: all five destinations in this picture are YouTube channels from the sports-club example; yours are one per platform you connect.",
        },
      },
    ],
    check: [
      {
        text: "If a platform's sign-in comes back empty or refused, the connecting guide has a section for it.",
        doc: "connecting-platforms",
        evidence: ["When something is empty or refused"],
        anchor: "when-something-is-empty-or-refused",
      },
      { ...METERS_AFTER_ROUTING },
    ],
    docs: ["connecting-platforms", "platforms", "quickstart", "obs"],
  },
  {
    slug: "streaming-from-a-backpack",
    route: "/recipes/streaming-from-a-backpack",
    title: "Streaming from a backpack — polyemesis recipe",
    description:
      "Receive a bonded encoder's links as one SRT publish. Tested with BELABOX's srtla_send, not yet with BELABOX hardware, Moblin or IRL Pro. Four steps.",
    heading: "Streaming from a backpack.",
    accent: "backpack",
    lede: "A bonded encoder's several links arrive as one ordinary SRT publish.",
    tag: "SRTLA · bonded encoders",
    needs: [
      {
        text: "A bonded IRL encoder that speaks SRTLA: BELABOX, Moblin and IRL Pro all bond this way.",
        doc: "srtla",
        evidence: ["BELABOX, Moblin and IRL Pro all bond this way"],
      },
      {
        text: "A source, added on the Sources page, with its protocol set to SRT and Apply pressed.",
        doc: "quickstart",
        evidence: ["Open the Sources page and add one", "pick its protocol", "SRT for multitrack", "press Apply"],
        anchor: "3-create-a-source",
      },
      { text: "One UDP port open to the server.", doc: "srtla", evidence: ["udp"] },
      {
        text: "The receiver was tested against BELABOX's reference sender, srtla_send. BELABOX hardware, Moblin and IRL Pro have not been tested against it yet; they speak the same protocol and are expected to work.",
        doc: "srtla",
        evidence: [
          "tested against BELABOX's reference sender",
          "BELABOX hardware, Moblin and IRL Pro have not been tested against this receiver yet",
          "expected to work",
        ],
        anchor: "compatibility-and-testing",
      },
    ],
    steps: [
      {
        title: "Turn the receiver on",
        facts: [
          {
            text: "In Settings → Ingest → Listeners, set the SRTLA bonding port; 5000 is the usual choice. 0, the default, leaves the receiver off.",
            doc: "srtla",
            evidence: [
              "Settings → Ingest → Listeners",
              "5000 is the usual choice",
              "0, the default, turns the receiver off",
            ],
            anchor: "setting-it-up",
          },
        ],
      },
      {
        title: "Open that port as UDP",
        facts: [
          {
            text: "Open the port as UDP in your firewall. In Docker, publish it, for example with -p 5000:5000/udp.",
            doc: "srtla",
            evidence: ["as UDP in your firewall", "-p 5000:5000/udp"],
            anchor: "setting-it-up",
          },
        ],
      },
      {
        title: "Point the encoder at it",
        facts: [
          {
            text: "In the encoder, choose SRTLA and enter this server's address, the SRTLA port, and the source's publish token as the stream ID. The token is shown on the source's card.",
            doc: "srtla",
            evidence: ["choose SRTLA", "publish token", "stream ID", "shown on the programme's source card"],
            anchor: "setting-it-up",
          },
          {
            text: "On BELABOX, use the custom SRTLA relay setting. On Moblin and IRL Pro, choose an SRTLA URL with your token as the stream ID.",
            doc: "srtla",
            evidence: ["custom SRTLA relay setting", "choose an SRTLA URL with your token as the stream ID"],
            anchor: "setting-it-up",
          },
        ],
      },
      {
        title: "Watch the links",
        facts: [
          {
            text: "The source card shows the merged stream (bitrate, RTT, loss) and one row per network link.",
            doc: "srtla",
            evidence: ["one row per network link", "RTT"],
            anchor: "what-you-see",
          },
        ],
      },
    ],
    check: [
      {
        text: "The SRTLA guide has a section on what to do if the backpack goes quiet.",
        doc: "srtla",
        evidence: ["If the backpack goes quiet"],
        anchor: "if-the-backpack-goes-quiet",
      },
      { text: "The guide lists the limits.", doc: "srtla", evidence: ["Limits"], anchor: "limits" },
    ],
    docs: ["srtla", "obs", "link-health"],
  },
];

export const RECIPE_ROUTES = Object.fromEntries(RECIPES.map((r) => [r.slug, r.route]));
