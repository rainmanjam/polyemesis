/* What the install page lists, as data the tests read. Ports and requirements
 * come from docs/INSTALL.md ("Ports", "Docker", "The FFmpeg requirement",
 * "Install the binary") and README.md; the first-run steps from
 * docs/QUICKSTART.md steps 1 to 7 and the old page's OBS steps.
 *
 * Version floors are never typed here. A requirement names one with a token,
 * {ffmpeg}, {go} or {node}, which download.astro fills from facts.mjs. */

import { SETUP_CODE } from "./install.mjs";

/** @type {{ port: string; proto: "tcp" | "udp"; what: string; when: string }[]} */
export const PORTS = [
  { port: "8080", proto: "tcp", what: "Web console and API", when: "Always. Docker publishes it for you. The bare binary listens on this machine only until you pass -addr." },
  { port: "6000", proto: "udp", what: "SRT ingest", when: "The default; changeable in Settings → Ingest. It is UDP." },
  { port: "1935", proto: "tcp", what: "RTMP ingest", when: "For encoders that cannot do SRT. One port however many RTMP sources you run." },
  { port: "443", proto: "tcp", what: "HTTPS for browsers", when: "Whenever polyemesis serves TLS itself." },
  { port: "80", proto: "tcp", what: "Let's Encrypt and redirect", when: "Only for tls.mode: acme, plus the HTTP to HTTPS redirect whenever polyemesis serves TLS." },
];

/** @type {{ name: string; value: string; note: string }[]} */
export const REQUIREMENTS = [
  {
    name: "Operating system",
    value: "Linux server and Docker (primary), macOS, Windows",
    note: "Linux and Docker are what the project develops and runs against. macOS suits a workstation. On Windows, build and evaluate.",
  },
  { name: "Docker", value: "Engine 20.10+ or Docker Desktop", note: "FFmpeg is bundled. Nothing else on the host." },
  {
    name: "FFmpeg without Docker",
    value: "{ffmpeg} or later, with srt",
    note: "polyemesis refuses to start on an older FFmpeg. Without srt it starts on RTMP only, which carries one audio track unless the encoder sends Enhanced RTMP.",
  },
  {
    name: "Build from a clone",
    value: "Go {go}+, Node {node}, FFmpeg",
    note: "make build builds the UI and embeds it.",
  },
  {
    name: "CPU",
    value: "About 4% of one core per destination",
    note: "Video is copied, not re-encoded. Only audio is decoded, mixed and re-encoded to AAC. Renditions are a real encode, and the UI estimates the cost before you save one.",
  },
  {
    name: "GPU",
    value: "Optional",
    note: "Only renditions re-encode video, and polyemesis tests each hardware encoder at startup and offers only what works.",
  },
  { name: "Bandwidth", value: "Your upload", note: "One upload per destination." },
];

/** @type {{ title: string; text: string; shot?: { file: string; alt: string; chrome: string }; code?: string }[]} */
export const FIRST_RUN = [
  {
    title: "Open the console",
    text: "Go to http://localhost:8080 in a browser. Installed with the script, the closing summary gives the address. A fresh install has no admin yet, so the first page is the setup screen.",
  },
  {
    title: "Create your admin",
    text: "Set a password and enter the one-time setup code. The server printed it when it started and saved it as setup-code in its data directory. It works once. If the password is ever lost, -reset-admin on the box sets a new one; there is no recovery from anywhere else.",
    code: SETUP_CODE,
  },
  {
    title: "Add a source",
    text: "A source is one programme: one feed from one encoder. On Sources, add one, pick SRT or RTMP on its card and press Apply. SRT carries every audio track. RTMP shares its port between sources, told apart by the stream key, and carries one audio track unless the publisher sends Enhanced RTMP. The publish URL appears with the protocol; copy it, because it carries the credential that addresses this source.",
    shot: { file: "05-sources.png", alt: "The Sources page with a source's protocol, publish URL and token", chrome: "Sources: protocol, publish URL and token" },
  },
  {
    title: "Point OBS at it",
    text: "In OBS, Settings → Output → Advanced → Recording, set Type to “Custom Output (FFmpeg)”, FFmpeg Output Type to “Output to URL”, container mpegts, and tick the audio tracks to send. Paste the publish URL, then press Start Recording. With Output to URL, that is the SRT push.",
  },
  {
    title: "Add a destination",
    text: "On Destinations, press Add and pick the platform. Paste its URL and stream key, or pick a connected account and let polyemesis fetch them. Tick the tracks this destination should carry, then enable it.",
    shot: { file: "18-destinations.png", alt: "The Destinations page, one card per destination with its tracks", chrome: "Destinations: one card per destination, tracks per card" },
  },
  {
    title: "Go live",
    text: "With OBS recording, the dashboard shows the ingest live within a couple of seconds, with a meter per incoming track. Start your destination and the dashboard reads ON AIR. Video is copied, so each extra destination costs almost nothing.",
    shot: { file: "01-dashboard.png", alt: "The dashboard on air, with the ingest live and a meter per track", chrome: "Dashboard: on air, ingest live" },
  },
];
