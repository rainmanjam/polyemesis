/* The receipts for what the /features page claims.
 *
 * Read as TEXT by internal/testenv/features_claim_test.go (a Go test), so the
 * layout is fixed: each entry's key, then on the very next line its receipt.
 * Keep one entry per claim, and keep that shape.
 *
 *   provenBy          a wrapper in ui/src/lib/api.ts that performs the action
 *                     the claim describes. The Go test asserts it exists and
 *                     that a console file outside api.ts calls it.
 *   claimIsBehaviour  something the server or a separate plugin does on its
 *                     own, so no console action proves it. Says why, in at
 *                     least 30 characters.
 *
 * src/lib/feature-claims.test.ts ties this list to the page: one claim per
 * in-depth section of features.astro (but not "also") and one per ALSO item
 * (keyed "also-<slug>"), none orphaned.
 *
 * The first six are the page's own sections; sections whose copy names several
 * actions carry the wrapper for the one the section leads with.
 */

/** @type {({ id: string, provenBy: string } | { id: string, claimIsBehaviour: string })[]} */
export const CLAIMS = [
  {
    id: "audio",
    provenBy: "compileRouting",
  },
  {
    id: "inputs",
    provenBy: "createSource",
  },
  {
    id: "failover",
    provenBy: "switchSource",
  },
  {
    id: "chat",
    provenBy: "deleteChatMessage",
  },
  {
    id: "recordings",
    provenBy: "submitRecordingJob",
  },
  {
    id: "deck",
    claimIsBehaviour:
      "The Stream Deck plugin is a separate package that calls the HTTP API with a paired token, so no console action performs it.",
  },
  {
    id: "also-dashboard",
    provenBy: "startDestination",
  },
  {
    id: "also-shared-renditions",
    provenBy: "createRendition",
  },
  {
    id: "also-meters-loudness",
    claimIsBehaviour:
      "Meters and loudness are measured by the server on the live signal and only displayed; the operator takes no action.",
  },
  {
    id: "also-live-captions",
    provenBy: "updateDestination",
  },
  {
    id: "also-broadcast-delay",
    provenBy: "updateDestination",
  },
  {
    id: "also-broadcast-from-file",
    provenBy: "putSettings",
  },
  {
    id: "also-platform-sign-in",
    provenBy: "connectUrl",
  },
  {
    id: "also-any-target",
    provenBy: "createDestination",
  },
  {
    id: "also-youtube-broadcast",
    claimIsBehaviour:
      "The server creates and ends the YouTube broadcast itself when the destination goes live or off; the operator only toggles the destination.",
  },
  {
    id: "also-obs-bridge",
    provenBy: "putObs",
  },
  {
    id: "also-own-player",
    provenBy: "playoutPublic",
  },
  {
    id: "also-audience",
    claimIsBehaviour:
      "The server polls each platform's viewer count and the player's own, and the console only displays them; there is no action to call.",
  },
  {
    id: "also-monitoring",
    claimIsBehaviour:
      "Host and process metrics are sampled by the server continuously and only displayed; there is no operator action to prove.",
  },
  {
    id: "also-prometheus",
    claimIsBehaviour:
      "The Prometheus endpoint and the in-process alert rules run on the server and are scraped from outside, not driven from the console.",
  },
  {
    id: "also-webhooks",
    claimIsBehaviour:
      "The server signs and sends one POST per lifecycle transition on its own; receiving them happens outside the console.",
  },
  {
    id: "also-home-assistant",
    claimIsBehaviour:
      "The server publishes retained MQTT telemetry with Home Assistant discovery on its own; consuming it happens in Home Assistant.",
  },
  {
    id: "also-tls",
    claimIsBehaviour:
      "The server obtains a Let's Encrypt certificate or makes its own CA at start-up from config.yaml; the console only reports the result.",
  },
  {
    id: "also-hot-reload",
    claimIsBehaviour:
      "The server applies most changes without restarting a process; that is how it reconciles, not an action the operator performs.",
  },
];
