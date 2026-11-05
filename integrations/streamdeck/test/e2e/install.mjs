// Seeds a freshly started polyemesis for the Stream Deck end-to-end run (scripts/streamdeck-e2e.sh), through
// the API an operator's browser uses: the first-run setup creates the admin account, the signed-in session
// mints an admin token and a read token the way the Tokens page does, and the admin token creates a programme
// and one custom RTMP destination pointing at the run's local sink. Nothing is written to the data directory
// by hand, and no credential is in the repository: the tokens exist only in this run's install.
//
// Usage: node install.mjs <base-url> <env-file> <sink-url>
// Writes shell `export` lines for the e2e test to <env-file>. Needs POLYEMESIS_SETUP_CODE, the code the server
// was started with.

import { writeFileSync } from "node:fs";

const [base, envFile, sinkURL] = process.argv.slice(2);
if (!base || !envFile || !sinkURL) {
  console.error("usage: node install.mjs <base-url> <env-file> <sink-url>");
  process.exit(2);
}
const api = `${base.replace(/\/$/, "")}/api/v1`;

function fail(what, res, body) {
  console.error(`install: ${what}: ${res ? `${res.status} ${body}` : body}`);
  process.exit(1);
}

/** Waits for the server to answer, observing it rather than sleeping a fixed time. */
async function waitUp() {
  const deadline = Date.now() + 60_000;
  let last = "";
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${api}/health`);
      if (res.ok) return;
      last = `HTTP ${res.status}`;
    } catch (err) {
      last = String(err?.cause?.code ?? err);
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  fail("the server never answered /health", undefined, last);
}

/** One JSON request; fails the run on anything but the status it expects. */
async function call(method, path, body, headers, want) {
  const res = await fetch(`${api}${path}`, {
    method,
    headers: { "Content-Type": "application/json", Accept: "application/json", ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  if (res.status !== want) fail(`${method} ${path}`, res, text);
  return { res, json: text ? JSON.parse(text) : undefined };
}

await waitUp();

const setupCode = process.env.POLYEMESIS_SETUP_CODE;
if (!setupCode) fail("setup", undefined, "POLYEMESIS_SETUP_CODE is not set");
const { res: setup } = await call(
  "POST",
  "/setup",
  { username: "admin", password: "deck-e2e-password", setupCode },
  {},
  201,
);

// The session the setup signed in, as the browser would carry it: the cookies, and the CSRF token from its cookie.
const cookies = setup.headers.getSetCookie().map((c) => c.split(";")[0]);
const csrf = cookies.find((c) => c.startsWith("polyemesis_csrf="))?.split("=")[1];
if (!csrf) fail("setup", undefined, "the setup answer set no CSRF cookie");
const session = { Cookie: cookies.join("; "), "X-CSRF-Token": csrf };

const mint = async (name, scope) =>
  (await call("POST", "/auth/tokens", { name, scope }, session, 201)).json.plaintext;
const admin = await mint("streamdeck-e2e", "admin");
const read = await mint("streamdeck-e2e-read", "read");
const bearer = { Authorization: `Bearer ${admin}` };

const id = (what, v) => (Number.isSafeInteger(v) && v > 0 ? v : fail(what, undefined, `the answer carried no id: ${JSON.stringify(v)}`));
const source = id("POST /sources", (await call("POST", "/sources", { name: "Main", enabled: true }, bearer, 201)).json?.id);

// Off until the deck starts it. One failed restart and it gives up, so a sink that goes away turns the key
// Failed within seconds instead of reconnecting for ever.
const destination = id("POST /destinations", (
  await call(
    "POST",
    "/destinations",
    {
      name: "Deck sink",
      kind: "rtmp",
      platform: "custom",
      url: sinkURL,
      enabled: false,
      audioBitrate: 128,
      sourceId: source,
      resilience: { giveUpAfter: 1 },
      profile: { mode: "simple", tracks: [{ track: 0, enabled: true, gain: 1 }], normalize: "auto", sampleRate: 48000 },
    },
    bearer,
    201,
  )
).json?.destination?.id);

const env = {
  POLY_E2E_URL: base,
  POLY_E2E_ADMIN_TOKEN: admin,
  POLY_E2E_READ_TOKEN: read,
  POLY_E2E_SOURCE: String(source),
  POLY_E2E_DESTINATION: String(destination),
  POLY_E2E_SINK_URL: sinkURL,
};
writeFileSync(envFile, Object.entries(env).map(([k, v]) => `export ${k}='${v}'\n`).join(""), { mode: 0o600 });
console.log(`install: admin and tokens created; programme ${source}, destination ${destination} -> ${sinkURL}`);
