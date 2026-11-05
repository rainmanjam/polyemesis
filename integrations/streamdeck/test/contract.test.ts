import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  API_CONTRACT,
  ApiError,
  PolyemesisClient,
  type Transport,
  type TransportRequest,
  type TransportResponse,
} from "../src/api.js";
import { KEY_STATE } from "../src/render.js";
import type { PollSpec } from "../src/model.js";
import { golden as goldenRoute, runtimeFor, settle, surface, type Route } from "./fakeInstall.js";
import { GOLDENS, SCHEMA, departures, golden, goldenResponse, violations, type Golden } from "./support/contract.js";

// THE PLUGIN AGAINST THE SERVER'S OWN ANSWERS.
//
// test/contract/ holds what the real handlers answered on a seeded install (internal/api/streamdeck_goldens_test.go):
// a programme with a measured layout and a live meter, and a destination in each state a key paints -- live, off,
// failed (connection refused), held (key rejected three times) and two buffering a delay. This file holds the
// plugin to those answers from three sides:
//
//   - the client sends exactly the request each case recorded: method, path, query and body;
//   - the client parses every 2xx answer, leaving nothing undefined or NaN;
//   - the runtime paints every key kind from them, and each key says what the server's state is.
//
// When the server changes an answer, the Go test fails until the goldens are regenerated, and then this file
// (and test/setup.ts's schema check on every fake) says what in the plugin the change breaks.

const BASE = new URL("https://studio.example");

/** Cases that record the server's answer to a request the plugin does not make. */
const NOT_SENT_BY_THE_PLUGIN = new Set([
  // The plugin always confirms a stop-all; this is the server's refusal of one that does not.
  "stop-all-unconfirmed",
  // A read token never sends a gated admin route (apiContract.json); this is what the server would answer.
  "destination-start-read-token",
]);

/** A transport that answers every request with one recorded answer, and keeps what was sent. */
function answering(res: TransportResponse) {
  const sent: TransportRequest[] = [];
  const transport: Transport = {
    async send(req) {
      sent.push(req);
      return { ...res };
    },
  };
  return { client: new PolyemesisClient(BASE, "t", transport), sent };
}

const num = (s: string | undefined) => (s === undefined ? undefined : Number(s));

/** Makes the call a case records, from the case's own request: the plugin's method for that route. */
function invoke(c: PolyemesisClient, g: Golden): Promise<unknown> {
  const q = g.request.query ?? {};
  const source = num(q.source);
  const seg = g.request.path.split("/");
  const body = (g.request.body ?? {}) as Record<string, unknown>;
  switch (`${g.request.method} ${g.request.route}`) {
    case "GET /auth/connection":
      return c.connection();
    case "GET /sources":
      return c.sources();
    case "GET /status":
      return c.status(source);
    case "GET /levels":
      return c.levels(source);
    case "GET /settings":
      return c.meterIntervalMs();
    case "GET /destinations/stop-impact":
      return q.destination ? c.stopImpactFor(Number(q.destination), source) : c.stopImpact(source);
    case "GET /recording":
      return c.recording();
    case "POST /recording":
      return c.setRecording(body.enabled as boolean);
    case "GET /polls":
      return c.currentPoll();
    case "GET /analytics/live":
      return c.analyticsLive();
    case "POST /polls":
      return c.startPoll(g.request.body as PollSpec);
    case "POST /polls/{id}/end":
      return c.endPoll(Number(seg[2]));
    case "GET /routing/presets":
      return c.routingPresets();
    case "POST /routing/presets/{presetId}/apply":
      return c.applyPreset(decodeURIComponent(seg[3]!), source);
    case "POST /destinations/start-all":
      return c.startAll(source);
    case "POST /destinations/stop-all":
      return c.stopAll(source);
    case "POST /destinations/{id}/start":
      return c.startDestination(Number(seg[2]), source);
    case "POST /destinations/{id}/restart":
      return c.restartDestination(Number(seg[2]), source);
    case "POST /destinations/{id}/stop":
      return c.stopDestination(Number(seg[2]), source, { now: q.now === "true" });
    case "POST /destinations/{id}/delay/dump":
      return c.dumpDelay(Number(seg[2]), body.seconds as number | undefined, source);
    case "POST /destinations/delay/dump-all":
      return c.dumpAll(source, body.seconds as number | undefined);
    case "POST /failover/source":
      return c.switchSource(body.source as string, source);
    case "GET /destinations":
      return c.destinations();
    case "GET /source":
      return c.trackNames(source);
    case "POST /destinations/{destinationId}/tracks/{track}/gain":
      return c.setTrackLevel(Number(seg[2]), Number(seg[4]), body as { muted?: boolean; gain?: number }, source);
  }
  throw new Error(`${g.case}: no client method for ${g.request.method} ${g.request.route}; add one to invoke()`);
}

/**
 * Properties the client leaves undefined on purpose: a viewer count a platform did not report is absent, never
 * 0 (src/model.ts ViewersLive).
 */
const ABSENT_BY_DESIGN = new Set(["$.total", "$.readings[].viewers"]);

/** Every NaN or explicitly undefined property in a parsed value, by path. */
function holes(v: unknown, path = "$"): string[] {
  if (typeof v === "number") return Number.isNaN(v) ? [`${path}: NaN`] : [];
  if (v instanceof Map) return [...v.entries()].flatMap(([k, e]) => holes(e, `${path}.get(${String(k)})`));
  if (Array.isArray(v)) return v.flatMap((e, i) => holes(e, `${path}[${i}]`));
  if (v && typeof v === "object") {
    return Object.entries(v).flatMap(([k, e]) => {
      if (e !== undefined) return holes(e, `${path}.${k}`);
      return ABSENT_BY_DESIGN.has(`${path.replace(/\[\d+\]/g, "[]")}.${k}`) ? [] : [`${path}.${k}: undefined`];
    });
  }
  return [];
}

const cases = [...GOLDENS.values()];

describe("the recorded answers", () => {
  it("are there, one or more for every route the plugin calls", () => {
    expect(cases.length).toBeGreaterThanOrEqual(30);
    const recorded = new Set(cases.map((g) => `${g.request.method} ${g.request.route}`));
    expect(API_CONTRACT.map((r) => `${r.method} ${r.path}`).filter((k) => !recorded.has(k))).toEqual([]);
    expect(API_CONTRACT.map((r) => `${r.method} ${r.path}`).filter((k) => !SCHEMA[k])).toEqual([]);
  });
});

describe("every fake answer is held to the server's schema (test/setup.ts)", () => {
  it("names a field the server does not send, and a value of a type it does not send there", () => {
    const status = SCHEMA["GET /status"];
    expect(departures({ destinations: [{ id: 1, process: { state: "failed", lastErr: "x" } }] }, status)).toEqual([
      "$.destinations[0].process.lastErr: not a field the server sends",
    ]);
    expect(departures({ destinations: [{ id: "1" }] }, status)).toEqual([
      "$.destinations[0].id: string, but the server sends number here",
    ]);
    // A field the server sends that a fake leaves out is fine: a fake may be partial.
    expect(departures({ destinations: [{ id: 1 }] }, status)).toEqual([]);
    // A refusal, on any route, has the server's one error shape.
    expect(departures({ message: "nope" }, SCHEMA.refusal)).toEqual(["$.message: not a field the server sends"]);
    expect(departures({ error: "nope", code: "no_source" }, SCHEMA.refusal)).toEqual([]);
  });

  it("fails the test that served it: the hook's afterEach is what enforces this", () => {
    // In a child vitest, so the failure it must produce is not this suite's.
    const root = fileURLToPath(new URL("..", import.meta.url));
    const vitest = fileURLToPath(new URL("../node_modules/vitest/vitest.mjs", import.meta.url));
    const run = spawnSync(process.execPath, [vitest, "run", "--config", "test/fixtures/vitest.fixture.config.ts"], {
      cwd: root,
      encoding: "utf8",
      env: { ...process.env, CI: "1", NO_COLOR: "1" },
    });
    const out = `${run.stdout}${run.stderr}`;
    expect(out).toContain("a fake answered with a shape the server does not send");
    expect(out).toContain("$.destinations[0].reason.isHeld: not a field the server sends");
    expect(run.status).toBe(1);
  }, 60_000);

  it("catches a renamed field in a fake served to the client, in any test", async () => {
    const renamed = { status: 200, body: JSON.stringify({ destinations: [{ id: 1, name: "x", reason: { cause: "dns", isHeld: true } }] }) };
    await answering(renamed).client.status(1);
    expect(violations.splice(0)).toEqual(["GET /status $.destinations[0].reason.isHeld: not a field the server sends"]);
  });
});

describe("the client sends what each case recorded", () => {
  for (const g of cases.filter((c) => !NOT_SENT_BY_THE_PLUGIN.has(c.case))) {
    it(g.case, async () => {
      const { client, sent } = answering(goldenResponse(g.case));
      await invoke(client, g).catch(() => undefined);
      expect(sent).toHaveLength(1);
      const req = sent[0]!;
      expect(req.method).toBe(g.request.method);
      expect(req.url.pathname).toBe(`/api/v1${g.request.path}`);
      expect(Object.fromEntries(req.url.searchParams)).toEqual(g.request.query ?? {});
      expect(req.body === undefined ? undefined : JSON.parse(req.body)).toEqual(g.request.body);
    });
  }
});

describe("the client parses every answer the server gives it", () => {
  for (const g of cases.filter((c) => c.status >= 200 && c.status < 300)) {
    it(g.case, async () => {
      const { client } = answering(goldenResponse(g.case));
      const out = await invoke(client, g);
      // Every 2xx answer recorded has a body, so every parser has something to return.
      expect(out).not.toBeUndefined();
      expect(holes(out)).toEqual([]);
    });
  }

  it("reads what each key needs out of them", async () => {
    const read = async <T>(name: string, call: (c: PolyemesisClient) => Promise<T>) => call(answering(goldenResponse(name)).client);
    expect(await read("connection-admin", (c) => c.connection())).toMatchObject({ scope: "admin", adminScope: true });
    expect(await read("connection-read", (c) => c.connection())).toMatchObject({ scope: "read", adminScope: false });
    expect(await read("sources", (c) => c.sources())).toEqual([{ id: 1 }]);
    const levels = await read("levels", (c) => c.levels(1));
    expect(levels.feed).toBe("live");
    expect(levels.levels?.peak[0]).toHaveLength(2);
    expect(await read("settings", (c) => c.meterIntervalMs())).toBeGreaterThan(0);
    expect(await read("recording", (c) => c.recording())).toMatchObject({ enabled: false, state: "off", installWide: true });
    expect(await read("recording-on", (c) => c.setRecording(true))).toMatchObject({ enabled: true });
    expect(await read("polls-running", (c) => c.currentPoll())).toMatchObject({ id: 1, question: "Next map?", state: "open" });
    expect(await read("polls-none", (c) => c.currentPoll())).toBeNull();
    expect(await read("analytics-live", (c) => c.analyticsLive())).toMatchObject({ enabled: true, onAir: true });
    const presets = await read("routing-presets", (c) => c.routingPresets());
    expect(presets.length).toBeGreaterThan(0);
    expect(presets.every((p) => p.id !== "" && p.name !== "")).toBe(true);
    const rows = await read("destinations", (c) => c.destinations());
    expect(rows.map((r) => r.name)).toEqual(["Main stage", "Backup encoder", "Studio relay", "YouTube main", "Delayed feed", "Delayed backup"]);
    expect(rows[0]).toMatchObject({ id: 1, sourceId: 1, matrix: false, copyAudio: false });
    expect(rows[0]!.tracks[0]).toEqual({ track: 0, enabled: true, gain: 1, muted: false });
    expect(await read("source", (c) => c.trackNames(1))).toEqual(new Map([[0, "Host mic"]]));
    const muted = await read("track-mute", (c) => c.setTrackLevel(1, 0, { muted: true }, 1));
    expect(muted.destination?.tracks[0]).toMatchObject({ track: 0, muted: true });
    const gained = await read("track-gain", (c) => c.setTrackLevel(1, 0, { gain: 0.5 }, 1));
    expect(gained.destination?.tracks[0]).toMatchObject({ track: 0, gain: 0.5 });
    expect(await read("delay-dump", (c) => c.dumpDelay(5, 10, 1))).toEqual({ id: 5, name: "Delayed feed", dumpedSeconds: 10 });
    const dumped = await read("delay-dump-all", (c) => c.dumpAll(1));
    expect(dumped.map((r) => r.outcome)).toEqual(["dumped", "skipped"]);
    expect(dumped[1]!.message).toMatch(/\w/);
  });
});

describe("a client paired with a read token", () => {
  // Every admin route, called the way its recorded case calls it, after a read token's connection check.
  for (const r of API_CONTRACT.filter((x) => x.scope === "admin")) {
    const g = cases.find((c) => c.request.method === r.method && c.request.route === r.path && c.status < 300)!;
    it(`${r.readTokenSends ? "sends" : "refuses, sending nothing,"} ${r.method} ${r.path}`, async () => {
      const sent: TransportRequest[] = [];
      const transport: Transport = {
        async send(req) {
          sent.push(req);
          return req.url.pathname.endsWith("/auth/connection") ? goldenResponse("connection-read") : goldenResponse("analytics-live-read-token");
        },
      };
      const client = new PolyemesisClient(BASE, "t", transport);
      await client.connection();
      const err = await invoke(client, g).catch((e: unknown) => e);
      expect(err).toBeInstanceOf(ApiError);
      expect((err as ApiError).kind).toBe("forbidden");
      expect(sent.slice(1).map((q) => `${q.method} ${q.url.pathname}`)).toEqual(r.readTokenSends ? [`${r.method} /api/v1${g.request.path}`] : []);
    });
  }
});

describe("the client reads the server's refusals the way the keys expect", () => {
  const refusal = async (name: string, call: (c: PolyemesisClient) => Promise<unknown>) =>
    (await call(answering(goldenResponse(name)).client).catch((e: unknown) => e)) as ApiError;

  it("an unknown token is unauthorized, which re-pairs", async () => {
    expect(await refusal("connection-unknown-token", (c) => c.connection())).toMatchObject({ kind: "unauthorized", status: 401 });
  });
  it("a read token's 403 is forbidden, painted as needs admin", async () => {
    expect(await refusal("analytics-live-read-token", (c) => c.analyticsLive())).toMatchObject({ kind: "forbidden", status: 403 });
    expect(await refusal("destination-start-read-token", (c) => c.startDestination(2, 1))).toMatchObject({ kind: "forbidden" });
  });
  it("an install with no programme is noSource, not a fault", async () => {
    expect(await refusal("no-source-start-all", (c) => c.startAll())).toMatchObject({ kind: "noSource", status: 503 });
  });
  it("a refused retry, an empty dump and an unconfirmed stop keep the server's own words", async () => {
    for (const [name, call] of [
      ["destination-retry-refused", (c: PolyemesisClient) => c.restartDestination(999, 1)],
      ["delay-dump-nothing", (c: PolyemesisClient) => c.dumpDelay(1, undefined, 1)],
      ["stop-all-unconfirmed", (c: PolyemesisClient) => c.stopAll(1)],
    ] as const) {
      const err = await refusal(name, call);
      expect(err).toBeInstanceOf(ApiError);
      expect(err.kind).toBe("http");
      expect(err.message).toBe((golden(name).body as { error: string }).error);
    }
  });
});

describe("every key kind, painted from the server's answers", () => {
  const install: Record<string, Route> = {
    "GET /api/v1/auth/connection": goldenRoute("connection-admin"),
    "GET /api/v1/sources": goldenRoute("sources"),
    "GET /api/v1/status": goldenRoute("status"),
    "GET /api/v1/levels": goldenRoute("levels"),
    "GET /api/v1/settings": goldenRoute("settings"),
    "GET /api/v1/recording": goldenRoute("recording"),
    "GET /api/v1/polls": goldenRoute("polls-running"),
    "GET /api/v1/analytics/live": goldenRoute("analytics-live"),
    "GET /api/v1/routing/presets": goldenRoute("routing-presets"),
    "GET /api/v1/destinations": goldenRoute("destinations"),
    "GET /api/v1/source": goldenRoute("source"),
    "GET /api/v1/destinations/stop-impact": goldenRoute("stop-impact"),
  };
  const preset = (golden("routing-presets").body as { presets: { id: string }[] }).presets[0]!.id;

  async function painted() {
    const { rt, net } = runtimeFor(install);
    rt.configure({ url: "https://studio.example", token: "t" });
    await rt.verify();
    const keys = {
      live: surface("d1", "destination", { destinationId: 1, sourceId: 1 }),
      off: surface("d2", "destination", { destinationId: 2, sourceId: 1 }),
      failed: surface("d3", "destination", { destinationId: 3, sourceId: 1 }),
      held: surface("d4", "destination", { destinationId: 4, sourceId: 1 }),
      delayed: surface("d5", "destination", { destinationId: 5, sourceId: 1 }),
      failover: surface("fo", "failover", { sourceId: 1 }),
      record: surface("rec", "record"),
      meter: surface("m", "meter", { sourceId: 1 }),
      viewers: surface("v", "viewers"),
      poll: surface("p", "poll", { pollQuestion: "Next map?", pollOptions: ["Dust", "Mirage"], pollDuration: 60 }),
      preset: surface("pr", "preset", { presetId: preset, sourceId: 1 }),
      mute: surface("tm", "trackMute", { destinationId: 1, track: 0, sourceId: 1 }),
      gain: surface("tg", "trackGain", { destinationId: 1, track: 0, sourceId: 1 }),
      stopOne: surface("s1", "stopOne", { destinationId: 1, sourceId: 1 }),
      stopAll: surface("sa", "stopAll", { sourceId: 1 }),
      goLive: surface("gl", "goLive", { sourceId: 1 }),
      dump: surface("du", "dump", { destinationId: 5, sourceId: 1 }),
    };
    for (const k of Object.values(keys)) rt.add(k);
    await rt.tick();
    await rt.tickMeters();
    await settle();
    return { keys, net };
  }

  it("leaves no key unpainted, and no face reads undefined, NaN or unknown", async () => {
    const { keys } = await painted();
    for (const [name, k] of Object.entries(keys)) {
      const shown = [k.titles.at(-1), k.feedback.at(-1)?.title, k.feedback.at(-1)?.value].filter((x) => x !== undefined);
      expect(shown.length, `${name} was never painted`).toBeGreaterThan(0);
      for (const text of shown) expect(text, name).not.toMatch(/undefined|NaN|unknown|\[object/i);
    }
  });

  it("paints each destination the state the server holds it in, with its cause", async () => {
    const { keys } = await painted();
    expect([keys.live.states.at(-1), keys.live.titles.at(-1)]).toEqual([KEY_STATE.live, "Live"]);
    expect([keys.off.states.at(-1), keys.off.titles.at(-1)]).toEqual([KEY_STATE.idle, "Off"]);
    expect([keys.failed.states.at(-1), keys.failed.titles.at(-1)]).toEqual([KEY_STATE.error, "Failed\nRefused"]);
    expect([keys.held.states.at(-1), keys.held.titles.at(-1)]).toEqual([KEY_STATE.held, "Held\nKey rejected"]);
    expect(keys.delayed.states.at(-1)).toBe(KEY_STATE.warn);
    expect(keys.delayed.titles.at(-1)).toMatch(/^Buffering delay\nDelay 900s$/);
    // Each destination key draws its platform mark and name.
    for (const k of [keys.live, keys.off, keys.failed, keys.held, keys.delayed]) expect(k.images.at(-1)).toMatch(/^data:image\/svg/);
  });

  it("paints the programme's keys from the same answers", async () => {
    const { keys } = await painted();
    expect(keys.failover.titles.at(-1)).toBe("Primary on air");
    expect(keys.record.titles.at(-1)).toBe("Record off");
    expect(keys.meter.feedback.at(-1)?.value).toMatch(/^-\d+ dB$/);
    expect(keys.poll.titles.at(-1)).toMatch(/^Poll on\n0 votes/);
    expect(keys.mute.titles.at(-1)).toBe("Unmuted\nHost mic");
    expect(keys.gain.feedback.at(-1)?.title).toBe("Main stage · Host mic");
    expect(keys.dump.titles.at(-1)).toMatch(/^Buffering delay\nDelayed feed/);
  });

  // The preset key used to send the compile route, which answered 200 and stored nothing, and showed OK. Held
  // now, it sends the apply route and shows what the server's answer says was saved -- and the Go side of this
  // case (deckPresetApplied) holds the server to having changed the stored routing.
  it("shows how many destinations the recorded preset apply saved, and OK only because none failed", async () => {
    const g = golden("preset-apply");
    const rows = (g.body as { results: { outcome: string }[] }).results;
    const saved = rows.filter((r) => r.outcome === "saved").length;
    expect(saved).toBeGreaterThan(0);
    expect(rows.some((r) => r.outcome === "failed")).toBe(false);
    const presetId = decodeURIComponent(g.request.path.split("/")[3]!);
    const { rt, net, sched } = runtimeFor({ ...install, [`POST /api/v1${g.request.path}`]: goldenRoute("preset-apply") });
    rt.configure({ url: "https://studio.example", token: "t" });
    await rt.verify();
    const key = surface("pr", "preset", { presetId, sourceId: 1 });
    rt.add(key);
    await rt.pressPresetDown("pr");
    sched.fireAll();
    await settle();
    expect(net.writes().map((r) => r.url.pathname)).toEqual([`/api/v1${g.request.path}`]);
    expect(key.titles.at(-1)).toBe(`Applied to ${saved}`);
    expect([key.oks, key.alerts]).toEqual([1, 0]);
  });
});
