/* The three examples the homepage, the audiences section and later pages all
 * show. Cities, track names and who gets what are illustrative, not real
 * traffic. Each destination lists the Track ids it carries; the video is
 * always carried (see mixLabel).
 *
 * @typedef {{ id: string, label: string }} Track
 * @typedef {{ name: string, city: string, lat: number, lon: number, tracks: string[] }} Dest
 * @typedef {{ key: "sports"|"worship"|"streamer", tab: string,
 *            source: { label: string, city: string, lat: number, lon: number, spec: string },
 *            tracks: Track[], dests: Dest[], fine: string }} Case */

/* The fine print, carried over word for word from the previous homepage. */
const LICENCE = "Use tracks you have the rights to.";
const TRANSLATION =
  "Translation comes from your own interpreters or translation service, as a track you send; polyemesis routes each language.";

/** @type {readonly ("sports"|"worship"|"streamer")[]} */
export const CASE_ORDER = ["sports", "worship", "streamer"];

/** "Español" -> "espanol", "Chat TTS" -> "chat-tts". */
const slug = (label) =>
  label.normalize("NFD").replace(/[̀-ͯ]/g, "").toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");

/** @param {string[]} labels @returns {Track[]} */
const tracksOf = (labels) => labels.map((label) => ({ id: slug(label), label }));

/** @type {Record<"sports"|"worship"|"streamer", Case>} */
export const CASES = {
  sports: {
    key: "sports",
    tab: "Sports club",
    source: { label: "match feed", city: "Manchester", lat: 53.48, lon: -2.24, spec: "OBS · SRT · 1080p60 · 6.0 Mbps" },
    tracks: tracksOf(["English", "Español", "Português", "Français", "Arabic", "Crowd"]),
    dests: [
      { name: "YouTube · English", city: "New York", lat: 40.71, lon: -74.0, tracks: ["english", "crowd"] },
      { name: "YouTube · Español", city: "Bogotá", lat: 4.71, lon: -74.07, tracks: ["espanol", "crowd"] },
      { name: "YouTube · Português", city: "São Paulo", lat: -23.55, lon: -46.63, tracks: ["portugues", "crowd"] },
      { name: "YouTube · Français", city: "Dakar", lat: 14.72, lon: -17.47, tracks: ["francais", "crowd"] },
      { name: "YouTube · Arabic", city: "Cairo", lat: 30.04, lon: 31.24, tracks: ["arabic", "crowd"] },
    ],
    fine: `${TRANSLATION} It doesn't license content. ${LICENCE}`,
  },
  worship: {
    key: "worship",
    tab: "Church & events",
    source: { label: "main hall", city: "Atlanta", lat: 33.75, lon: -84.39, spec: "hardware encoder · SRT · 1080p30 · 4.0 Mbps" },
    tracks: tracksOf(["Room mix", "Broadcast mix", "Español", "Português", "Music only"]),
    dests: [
      { name: "Custom RTMP", city: "Lisbon", lat: 38.72, lon: -9.14, tracks: ["room-mix"] },
      { name: "YouTube", city: "London", lat: 51.51, lon: -0.13, tracks: ["broadcast-mix"] },
      { name: "Facebook", city: "Bogotá", lat: 4.71, lon: -74.07, tracks: ["espanol"] },
      { name: "X", city: "São Paulo", lat: -23.55, lon: -46.63, tracks: ["portugues"] },
      { name: "Kick", city: "Dakar", lat: 14.72, lon: -17.47, tracks: ["music-only"] },
    ],
    fine: `${TRANSLATION} It doesn't license music. ${LICENCE}`,
  },
  streamer: {
    key: "streamer",
    tab: "Streamer",
    source: { label: "home studio", city: "Berlin", lat: 52.52, lon: 13.4, spec: "OBS · SRT · 1080p60 · 8.0 Mbps" },
    tracks: tracksOf(["Mic", "Game", "Music", "Alerts", "Chat TTS"]),
    dests: [
      { name: "Twitch", city: "London", lat: 51.51, lon: -0.13, tracks: ["mic", "game", "music", "alerts", "chat-tts"] },
      { name: "YouTube", city: "Dubai", lat: 25.2, lon: 55.27, tracks: ["mic", "game", "alerts"] },
      { name: "Kick", city: "Johannesburg", lat: -26.2, lon: 28.05, tracks: ["mic", "game", "music"] },
      { name: "Facebook", city: "Cairo", lat: 30.04, lon: 31.24, tracks: ["mic", "alerts", "chat-tts"] },
      { name: "X", city: "Lagos", lat: 6.52, lon: 3.38, tracks: ["mic", "game"] },
    ],
    fine: `polyemesis routes audio; it doesn't license music. ${LICENCE}`,
  },
};

/** What a destination receives: "video + ENGLISH + CROWD". @param {Case} c @param {Dest} dest */
export function mixLabel(c, dest) {
  const names = dest.tracks.map((id) => c.tracks.find((t) => t.id === id)?.label.toUpperCase() ?? id.toUpperCase());
  return ["video", ...names].join(" + ");
}
