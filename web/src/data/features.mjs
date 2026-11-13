/* The copy of the /features page, as data, so tests can read it.
 *
 * Everything here comes from the previous features page, from docs/, or from
 * the design mockup where one of those already says the same thing. Nothing is
 * invented. The page (pages/features.astro) renders it.
 *
 * SHAPE RULE: SCREENS is read as text by src/lib/features-shots.test.ts, which
 * looks for each entry's key, then its shot, then its alt text, in that order.
 * No other array in this file uses that first key, so the other lists key
 * their entries by `slug`, `group` or `verb`.
 */

/** The four sections that carry a console screenshot, in page order.
 * The audio section's caption is built on the page from TRACK_LAYOUT.
 * @type {{ id: string, shot: string, alt: string, caption?: string }[]} */
export const SCREENS = [
  {
    id: "audio",
    shot: "02-routing.png",
    alt: "polyemesis routing page: per-destination track selection with music policy and loudness target",
  },
  {
    id: "inputs",
    shot: "05-sources.png",
    alt: "polyemesis sources page: two programmes each showing signal detected, an SRT address on port 6000 and a masked publishing token, with a third programme's header below",
    caption:
      "Three programmes on one SRT port: each publishes to port 6000 with its own token (masked here).",
  },
  {
    id: "chat",
    shot: "09-chat.png",
    alt: "polyemesis chat timeline merging YouTube, Twitch, Kick and Facebook messages, with platform toggles above, a poll result marked as sent by polyemesis, and a send box whose chips show YouTube and Twitch selected and Kick not",
    caption:
      "One timeline across four connected platforms with a toggle for each, and a send box that goes to the live platforms by default, with a chip to add any other.",
  },
  {
    id: "recordings",
    shot: "15-recordings.png",
    alt: "polyemesis recordings page: segment list with tracks, stems, retention settings and disk usage",
    caption: "Segments with every ingest track preserved, next to retention by age and size and the free-space floor.",
  },
];

/** The lead section.
 * @type {{ eyebrow: string, heading: string, accent: string, body: string[] }} */
export const LEAD = {
  eyebrow: "Audio routing",
  heading: "Every destination, its own mix.",
  accent: "mix",
  body: [
    "One encoder sends up to 32 audio tracks, and every destination gets its own mix of them, from one upload and one video encode. Two languages, home and away commentary, a clean game feed: each is a destination that takes the tracks it needs.",
    "polyemesis ingests once and lets you pick, per destination, which tracks get summed into the stereo feed that destination receives.",
  ],
};

/** The five rooms: who routes what where.
 * @type {{ title: string, body: string, routes: [string, string][], note?: string, link?: { href: string, text: string } }[]} */
export const ROOMS = [
  {
    title: "Two languages, one camera",
    body: "The floor mic on one track, the interpreter on another. Each language channel takes its own track, and both get the same video from one upload. A third language is a checkbox and another destination, not another encoder.",
    routes: [
      ["track 1 · floor mic", "English channel"],
      ["track 2 · interpreter", "Spanish channel"],
    ],
    note: "Churches, conferences and any event with more than one language on one stream.",
  },
  {
    title: "An archive that differs from the broadcast",
    body: "The live broadcast carries the music bed. A second mix, built from the same ingest, leaves it out for the recording or a file destination. One upload, one video encode.",
    routes: [
      ["the mix with the music bed", "live broadcast"],
      ["the mix without it", "archive, or the Twitch VOD track"],
    ],
    note: "The Twitch route needs a GPU and is Twitch-only.",
    link: { href: "/twitch-vod-track", text: "What that involves" },
  },
  {
    title: "Vertical and horizontal, at once",
    body: "A 16:9 composition and a 9:16 one are two programmes, not one cropped from the other. Run both from one install, each with its own ingest, destinations and mix.",
    routes: [
      ["16:9 programme", "horizontal destinations"],
      ["9:16 programme", "vertical destinations"],
    ],
  },
  {
    title: "Home and away, one ingest",
    body: "Home commentary on one track, away commentary on another, the game's own audio on a third. The home feed takes the game with home commentary, the away feed takes it with away commentary, and the clean league feed takes the game alone.",
    routes: [
      ["tracks 1 + 3 · home commentary + game", "home feed"],
      ["tracks 2 + 3 · away commentary + game", "away feed"],
      ["track 3 · game audio", "clean league feed"],
    ],
  },
  {
    title: "Esports, game and casters",
    body: "The game on one track and the casters on another. The main broadcast gets both, and a platform that wants no commentary gets the game alone, from the same ingest.",
    routes: [
      ["track 1 · game audio", "clean game feed"],
      ["track 2 · casters", "casters channel"],
      ["tracks 1 + 2", "main broadcast"],
    ],
  },
];

/** What each input carries into per-destination audio: a header row, then one row per input.
 * @type {string[][]} */
export const INPUTS = [
  ["Input", "Audio tracks", "Good for"],
  [
    "SRT",
    "up to 32 (six from OBS)",
    "Multitrack from OBS using a Custom Output (FFmpeg) to URL; the token rides in the streamid",
  ],
  [
    "RTMP",
    "one stereo pair; more over Enhanced RTMP",
    "Any encoder that speaks RTMP; the token goes in OBS's Stream Key field",
  ],
  [
    "SRTLA",
    "as SRT",
    "Bonded IRL encoders (BELABOX, Moblin, IRL Pro) over several links; arrives as an ordinary SRT publish",
  ],
];

/** Failover: the ladder, the order's reason, the hand controls, the thresholds.
 * @type {string[]} */
export const FAILOVER_FACTS = [
  "The ladder: primary ingest, standby ingest, scheduled file, generated slate. The platform sees one continuous connection while the source underneath changes.",
  "A scheduled file is a fallback for nobody streaming, so it sits below a live encoder and above the slate.",
  "By hand: pin the source to primary, backup or slate, or hand it back to auto.",
  "The primary counts as degraded when it stays below your floor: 800 kbps, 1500 ms round-trip time or 15% packet loss, held for 5 seconds. Those are the defaults, and 0 turns a threshold off. Round-trip time and loss are measured on SRT and SRTLA only.",
  "Off by default: turn it on in Settings → Pipeline → Failover.",
  "Stop on ingest loss stops the live destinations instead of airing dead video, for any encoder. It is off until you turn it on.",
];

/** Chat and moderation, one labelled row each.
 * @type {{ label: string, text: string }[]} */
export const CHAT_FACTS = [
  {
    label: "Read",
    text: "YouTube, Twitch, Kick, Facebook and Rumble in one merged timeline (Rumble by polling).",
  },
  {
    label: "Send",
    text: "To YouTube, Twitch and Kick, by default only the platforms that are live, with per-platform verdicts when half the send fails.",
  },
  {
    label: "Moderate",
    text: "Delete everywhere, hide on Facebook, ban and timeout on YouTube, Twitch and Kick.",
  },
  {
    label: "Moderator card",
    text: "What one person has said across every platform at once.",
  },
  {
    label: "Automatic rules",
    text: "Patterns and rate detectors run in the process; a model checker is optional and off by default.",
  },
  {
    label: "Permissions",
    text: "What a rule may do is granted per platform and per action, in a matrix that starts empty.",
  },
];

/** What happens to a recording, as four verbs.
 * @type {{ verb: string, text: string }[]} */
export const RECORDING_VERBS = [
  { verb: "Record", text: "Segmented, stream-copied, every audio track kept." },
  {
    verb: "Offload",
    text: "Finished recordings copied to an S3-compatible bucket: AWS S3, Cloudflare R2, Backblaze B2, MinIO or Wasabi.",
  },
  { verb: "Transcribe", text: "Whisper on a queue that yields to the live stream; the transcript is searchable." },
  {
    verb: "Clip",
    text: "Highlights suggested from chat bursts and loudness peaks, and 9:16 shorts with burned-in captions.",
  },
];

/** The Stream Deck plugin's requirements and guards.
 * @type {string[]} */
export const DECK_FACTS = [
  "Needs the Stream Deck app 7.1 or later, on macOS 12+ or Windows 10+.",
  "A Stream Deck + has eight keys, a touch strip and four dials; the Meter and Track gain dials, and Viewers on a dial, need it, and every other action is a key that works on any model.",
  "Pair with a read token to show state or an admin token to change it.",
  "Stops are holds: Stop all and Stop destination show how many YouTube broadcasts the stop would end.",
  "Connects over HTTPS to the install; plain http is accepted only for localhost.",
  "The packaged plugin is attached to every release since 0.12.0.",
];

/** Everything else, in four groups; `doc` is a slug in data/docs.mjs.
 * @type {{ group: string, blurb: string, items: { name: string, text: string, doc: string }[] }[]} */
export const ALSO = [
  {
    group: "On air",
    blurb: "what the broadcast does",
    items: [
      {
        name: "Dashboard",
        text: "The programme you picked in the header: its ingest (bitrate, uptime, how many audio tracks arrived, and the address to publish to), the live picture, the pipeline behind it, and every destination it feeds, each with the tracks it carries and a button to start or stop it.",
        doc: "quickstart",
      },
      {
        name: "Shared renditions",
        text: "One encode feeds every destination that selects it, ref-counted. It re-encodes video only, so audio still routes per destination.",
        doc: "renditions",
      },
      {
        name: "Live meters and loudness",
        text: "A meter on every channel of every track, and loudness measured per destination, after routing, from one ingest.",
        doc: "audio-routing",
      },
      {
        name: "Live captions",
        text: "Transcribed on your own machine, with a caption route per destination where the platform offers one. Every destination starts with captions off.",
        doc: "captions",
      },
      {
        name: "Broadcast delay",
        text: "Hold a destination back by up to 15 minutes.",
        doc: "broadcast-delay",
      },
      {
        name: "Broadcast from a file",
        text: "A file that plays on a loop as the ingest, switched on by a schedule.",
        doc: "scheduled-broadcast",
      },
    ],
  },
  {
    group: "Platforms getting connected",
    blurb: "sign-in, keys and destinations",
    items: [
      {
        name: "Platform sign-in",
        text: "Connect YouTube, Twitch, Facebook or Kick and polyemesis fetches the stream key itself, then pushes your title, category and description at go-live. Nothing proxies your video.",
        doc: "connecting-platforms",
      },
      {
        name: "Any RTMP, RTMPS or SRT target",
        text: "A pasted key is a fully supported destination, not a degraded one: twenty-three other presets in the catalogue work exactly that way, over RTMP, RTMPS or SRT.",
        doc: "platforms",
      },
      {
        name: "A YouTube broadcast made for you",
        text: "A destination linked to your channel gets its own broadcast when it goes live, bound to your stored stream key, which is never changed. It ends when you switch the destination off.",
        doc: "connecting-platforms",
      },
      {
        name: "OBS bridge",
        text: "OBS starting a stream starts the destinations you picked; a scene change applies a routing preset.",
        doc: "obs-bridge",
      },
    ],
  },
  {
    group: "Viewers",
    blurb: "who is watching",
    items: [
      {
        name: "Your own player",
        text: "A player page polyemesis serves itself, public or protected by a playback token, beside the fan-out rather than instead of it.",
        doc: "quickstart",
      },
      {
        name: "Audience across platforms",
        text: "Viewer counts from YouTube, Twitch, Kick and your own player on the Dashboard while you are live, and a report per stream afterwards.",
        doc: "api",
      },
    ],
  },
  {
    group: "Operations",
    blurb: "keeping it running",
    items: [
      {
        name: "Monitoring",
        text: "Host CPU, memory, relay throughput, ingest loss and the live FFmpeg output of each process.",
        doc: "monitoring",
      },
      {
        name: "Prometheus metrics and alerts",
        text: "Everything the dashboard draws is also available as Prometheus metrics, plus in-process alert rules with webhook delivery.",
        doc: "grafana",
      },
      {
        name: "Signed lifecycle webhooks",
        text: "One signed POST per transition, in order.",
        doc: "hooks",
      },
      {
        name: "Home Assistant",
        text: "Retained MQTT telemetry with Home Assistant discovery.",
        doc: "mqtt",
      },
      {
        name: "TLS that configures itself",
        text: "Let's Encrypt on a public DNS name; a self-signed CA otherwise.",
        doc: "tls",
      },
      {
        name: "Changes that do not drop the stream",
        text: "Most of what an operator can change applies without touching a process.",
        doc: "hot-reload",
      },
    ],
  },
];
