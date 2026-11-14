/* The three recipe pages (/recipes/...): their metadata, what each needs, the
 * four steps, how to check it worked and the docs worth reading next. Every
 * product sentence is a Fact (lib/facts.ts): the doc it comes from and the
 * phrases that tie it to that doc, checked against docs/ by recipes-data.test.ts.
 * Step titles are the homepage's (components/home/Use.astro) without the final
 * period; the homepage test keeps the two in step.
 *
 * A recipe is complete (ruling P4-R12): inside its four steps it carries every
 * action the docs require to get from the step before to the step after, so a
 * reader following only the page reaches what the title says. The completeness
 * guards in recipes-data.test.ts pin the gaps a review found.
 */

import * as SETUP from "./setup-facts.mjs";

/**
 * @typedef {import("../lib/facts").Fact} Fact
 * @typedef {import("../lib/howto").Recipe} Recipe */

/** @type {Recipe[]} */
export const RECIPES = [
  {
    slug: "two-languages-one-broadcast",
    route: "/recipes/two-languages-one-broadcast",
    title: "Two languages, one broadcast — polyemesis recipe",
    description:
      "Give each voice its own OBS track, send them as one SRT stream, and give each destination the language it needs. Four steps from a working install.",
    heading: "Two languages, one broadcast.",
    crumbLabel: "two languages, one broadcast",
    accent: "one",
    lede: "Each voice gets its own track, and each destination gets the language it needs, from one upload.",
    tag: "audio routing · OBS",
    needs: [
      { text: "Up to six tracks from OBS.", doc: "audio-routing", evidence: ["six is what OBS sends"] },
      SETUP.SRT_NEED,
    ],
    steps: [
      {
        title: "Give each voice its own track",
        facts: [
          SETUP.OBS_BITRATES,
          SETUP.OBS_TRACK_TICKS,
        ],
      },
      {
        title: "Send it as one SRT stream",
        facts: [
          SETUP.ADD_SOURCE,
          SETUP.COPY_URL,
          SETUP.OBS_CUSTOM_OUTPUT,
          SETUP.PASTE_URL,
          SETUP.AUDIO_TRACKS,
          SETUP.START_RECORDING,
          SETUP.METERS_APPEAR,
        ],
      },
      {
        title: "Label the tracks",
        facts: [SETUP.LABEL_TRACKS],
      },
      {
        title: "One destination per language",
        facts: [SETUP.ADD_DESTINATION, SETUP.URL_AND_KEY, SETUP.TICK_TRACKS, SETUP.ENABLE_DESTINATION],
        shot: {
          ...SETUP.DESTINATIONS_SHOT,
          alt: "The dashboard's Destinations section, one card per destination showing the tracks it carries",
        },
      },
    ],
    check: [
      {
        text: "The dashboard should show your track count and video format.",
        doc: "obs",
        evidence: ["The polyemesis dashboard should show your track count and video format"],
        anchor: "5-verify",
      },
      {
        text: "Open Audio meters and confirm each track carries what you think it carries.",
        doc: "audio-routing",
        evidence: ["Open Audio meters and confirm each track carries what you think it carries"],
        anchor: "verifying-it-before-you-go-live",
      },
      SETUP.METERS_AFTER_ROUTING,
      SETUP.SILENCE_FACT,
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
    crumbLabel: "OBS to five platforms",
    accent: "platforms",
    lede: "One connection from OBS to your server, and a destination for each platform.",
    tag: "OBS · connecting platforms",
    needs: [
      SETUP.CONNECT_NEEDS_HOSTNAME,
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
        facts: [SETUP.ADD_SOURCE, SETUP.COPY_URL, SETUP.OBS_CUSTOM_OUTPUT, SETUP.PASTE_URL, SETUP.START_RECORDING],
      },
      {
        title: "Connect each platform",
        facts: [SETUP.APP_REQUIREMENTS, SETUP.REDIRECT_URL, SETUP.REGISTER_APP, SETUP.PASTE_CREDENTIALS, SETUP.SAME_FOUR_STEPS],
      },
      {
        title: "Add a destination per platform",
        facts: [SETUP.ADD_DESTINATION, SETUP.URL_AND_KEY, SETUP.TICK_TRACKS, SETUP.ENABLE_DESTINATION],
        shot: {
          ...SETUP.DESTINATIONS_SHOT,
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
      SETUP.METERS_AFTER_ROUTING,
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
    crumbLabel: "streaming from a backpack",
    accent: "backpack",
    lede: "A bonded encoder's several links arrive as one ordinary SRT publish.",
    tag: "SRTLA · bonded encoders",
    needs: [
      {
        text: "A bonded IRL encoder that speaks SRTLA: BELABOX, Moblin and IRL Pro all bond this way.",
        doc: "srtla",
        evidence: ["BELABOX, Moblin and IRL Pro all bond this way"],
      },
      SETUP.ADD_SOURCE,
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
        text: "The stream arrives at your programme as an ordinary SRT publish, so failover, recording, destinations and degraded-link failover work the same.",
        doc: "srtla",
        evidence: [
          "the stream arrives at your programme as an ordinary SRT publish",
          "Failover, recording, destinations and degraded-link failover all work the same",
        ],
      },
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
