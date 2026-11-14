/* The three recipe pages (/recipes/...): their metadata, what each needs, the
 * four steps, how to check it worked and the docs worth reading next. Every
 * product sentence is a Fact (lib/facts.ts): the doc it comes from and the
 * phrases that prove it, checked against docs/ by recipes-data.test.ts.
 * Step titles are the homepage's (components/home/Use.astro) without the final
 * period; the homepage test keeps the two in step.
 */

/**
 * @typedef {import("../lib/facts").Fact} Fact
 * @typedef {import("../lib/howto").Recipe} Recipe */

/** The picture's label where it borrows the sports-club demo install. */
const DEMO_CAPTION =
  "Demo install: the destinations and tracks in this picture are an example; yours are the ones you add and label.";

/** @type {Fact} */
const OUTPUT_TO_URL = {
  text: "Use the Recording output set to Output to URL, container mpegts, and press Start Recording.",
  doc: "obs",
  evidence: ["Output to URL", "mpegts", "Start Recording"],
  anchor: "multitrack-over-srt",
};
/** @type {Fact} */
const METERS_AFTER_ROUTING = {
  text: "The Meters page shows loudness measured after routing.",
  doc: "quickstart",
  evidence: ["measured after routing"],
  anchor: "7-watch-that-it-is-actually-right",
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
      { text: "Six tracks from OBS.", doc: "audio-routing", evidence: ["six is what OBS sends"] },
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
            text: "In OBS, open Advanced Audio Properties and tick which of the six tracks each source feeds.",
            doc: "obs",
            evidence: ["Advanced Audio Properties", "six"],
            anchor: "multitrack-over-srt",
          },
        ],
      },
      {
        title: "Send it as one SRT stream",
        facts: [
          OUTPUT_TO_URL,
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
            text: "Tick the tracks each destination carries.",
            doc: "quickstart",
            evidence: ["tick the ones this destination should carry"],
            anchor: "6-add-a-destination",
          },
          {
            text: "Then check the Meters page, which measures after routing.",
            doc: "quickstart",
            evidence: ["measured after routing"],
            anchor: "7-watch-that-it-is-actually-right",
          },
        ],
        shot: {
          file: "18-destinations.png",
          alt: "The Destinations page, one card per destination showing the tracks it carries",
          chrome: "Destinations",
          caption: DEMO_CAPTION,
        },
      },
    ],
    check: [
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
    howto: {
      name: "Send two languages from one broadcast",
      tools: ["OBS Studio"],
      supplies: ["A running polyemesis server"],
    },
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
        text: "A developer app on each platform you connect.",
        doc: "connecting-platforms",
        evidence: ["Register an app"],
      },
      { text: "OBS set up for Output to URL.", doc: "obs", evidence: ["Output to URL"] },
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
      { title: "Point OBS at it", facts: [OUTPUT_TO_URL] },
      {
        title: "Connect each platform",
        facts: [
          {
            text: "Register an app in the platform's developer console, paste its credentials into Settings → Platform credentials, and press Connect account.",
            doc: "connecting-platforms",
            evidence: ["Register an app", "Settings → Platform credentials", "Connect account"],
            anchor: "the-same-four-steps-on-every-platform",
          },
          {
            text: "The same four steps work for X, Twitch, Kick, YouTube and Facebook; Facebook is still being verified.",
            doc: "connecting-platforms",
            evidence: ["The same four steps on every platform", "Facebook (being verified)"],
          },
        ],
      },
      {
        title: "Add a destination per platform",
        facts: [
          {
            text: "Under Destinations → Add, pick the platform, then enable it.",
            doc: "quickstart",
            evidence: ["Destinations → Add", "Enable"],
            anchor: "6-add-a-destination",
          },
          {
            text: "Choose the connected account for that platform.",
            doc: "connecting-platforms",
            evidence: ["connected account"],
          },
        ],
        shot: {
          file: "18-destinations.png",
          alt: "The Destinations page, one card per destination, each live",
          chrome: "Destinations",
          caption: DEMO_CAPTION,
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
    howto: {
      name: "Send OBS to several platforms through polyemesis",
      tools: ["OBS Studio"],
      supplies: ["A running polyemesis server"],
    },
  },
  {
    slug: "streaming-from-a-backpack",
    route: "/recipes/streaming-from-a-backpack",
    title: "Streaming from a backpack — polyemesis recipe",
    description:
      "Receive a bonded IRL encoder over several links as one ordinary SRT publish. Works with BELABOX, Moblin and IRL Pro. Four steps from a working install.",
    heading: "Streaming from a backpack.",
    accent: "backpack",
    lede: "A bonded encoder's several links arrive as one ordinary SRT publish.",
    tag: "SRTLA · bonded encoders",
    needs: [
      {
        text: "A bonded IRL encoder: BELABOX, Moblin or IRL Pro.",
        doc: "srtla",
        evidence: ["BELABOX", "Moblin", "IRL Pro"],
      },
      { text: "One UDP port open to the server.", doc: "srtla", evidence: ["udp"] },
    ],
    steps: [
      {
        title: "Turn the receiver on",
        facts: [
          {
            text: "In Settings → Ingest → Listeners, set the SRTLA bonding port; 5000 is the usual choice.",
            doc: "srtla",
            evidence: ["Settings → Ingest → Listeners", "5000 is the usual choice"],
            anchor: "setting-it-up",
          },
        ],
      },
      {
        title: "Open that port as UDP",
        facts: [
          { text: "In Docker, publish it with -p 5000:5000/udp.", doc: "srtla", evidence: ["-p 5000:5000/udp"], anchor: "setting-it-up" },
        ],
      },
      {
        title: "Point the encoder at it",
        facts: [
          {
            text: "In BELABOX, Moblin or IRL Pro, choose SRTLA and enter your server, the port, and the source's publish token as the stream ID.",
            doc: "srtla",
            evidence: ["BELABOX", "Moblin", "IRL Pro", "publish token", "stream ID"],
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
    howto: {
      name: "Receive a bonded encoder with SRTLA",
      tools: ["A bonded IRL encoder: BELABOX, Moblin or IRL Pro"],
      supplies: ["A running polyemesis server"],
    },
  },
];

export const RECIPE_ROUTES = Object.fromEntries(RECIPES.map((r) => [r.slug, r.route]));
