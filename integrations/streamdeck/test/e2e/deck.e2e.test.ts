import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { readFileSync } from "node:fs";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import type { Transport, TransportRequest } from "../../src/api.js";
import { HOLD_MS, Runtime } from "../../src/runtime.js";
import { KEY_STATE } from "../../src/render.js";
import { nodeTransport } from "../../src/transport.js";
import { manualScheduler, surface } from "../fakeInstall.js";

// THE PLUGIN'S RUNTIME AGAINST A REAL POLYEMESIS, OVER REAL HTTP.
//
// scripts/streamdeck-e2e.sh starts the real binary on a temporary data directory, creates the admin, an admin
// token and a read token through the API, and seeds a programme and one custom RTMP destination pointing at a
// local sink (test/e2e/install.mjs). This file is the deck: the same Runtime the plugin runs, with node's own
// transport, painting fake key surfaces. It owns the media: a synthetic feed into the programme's relay, and an
// RTMP sink it starts, kills and starts again.
//
// Every wait is for a state: a key face, a sink that has received media, a process that has exited. None is a
// fixed sleep, and each has a deadline that names what it was waiting for.

const env = (name: string): string => {
  const v = process.env[name];
  if (!v) throw new Error(`${name} is not set: run this through scripts/streamdeck-e2e.sh, not on its own`);
  return v;
};
const URL_ = env("POLY_E2E_URL");
const ADMIN = env("POLY_E2E_ADMIN_TOKEN");
const READ = env("POLY_E2E_READ_TOKEN");
const id = (name: string): number => {
  const n = Number(env(name));
  if (!Number.isSafeInteger(n) || n <= 0) throw new Error(`${name} is ${env(name)}, not an id`);
  return n;
};
const SOURCE = id("POLY_E2E_SOURCE");
const DEST = id("POLY_E2E_DESTINATION");
const SINK_URL = env("POLY_E2E_SINK_URL");
const FFMPEG = process.env.POLY_E2E_FFMPEG ?? "ffmpeg";
/** A marker on every process this file starts, so the script's teardown can find exactly ours. */
const MARKER = env("POLY_E2E_MARKER");

/** Polls a condition until it holds, or fails naming what it waited for. */
async function until(what: string, ms: number, cond: () => boolean | Promise<boolean>, describeState?: () => string): Promise<void> {
  const deadline = Date.now() + ms;
  while (Date.now() < deadline) {
    if (await cond()) return;
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(`timed out after ${ms} ms waiting for ${what}${describeState ? `; last seen: ${describeState()}` : ""}`);
}

/** One of this file's child processes, with its output kept and its exit observable. */
interface Child {
  proc: ChildProcess;
  out: string;
  exited: boolean;
}

const children: Child[] = [];

function start(args: string[]): Child {
  const proc = spawn(FFMPEG, args, { stdio: ["ignore", "pipe", "pipe"] });
  const child: Child = { proc, out: "", exited: false };
  proc.stdout?.on("data", (b: Buffer) => (child.out += b.toString()));
  proc.stderr?.on("data", (b: Buffer) => (child.out += b.toString()));
  proc.on("exit", () => (child.exited = true));
  children.push(child);
  return child;
}

async function stop(child: Child, what: string): Promise<void> {
  if (child.exited) return;
  child.proc.kill("SIGKILL");
  await until(`${what} to exit`, 10_000, () => child.exited);
}

const SINK_PORT = Number(new URL(SINK_URL).port);

/**
 * Whether something is listening on the sink's port, read from the kernel's socket table rather than by
 * connecting: the sink takes exactly one connection, and a probe would be it.
 */
function listening(port: number): boolean {
  if (process.platform === "linux") {
    const hex = port.toString(16).toUpperCase().padStart(4, "0");
    return ["/proc/net/tcp", "/proc/net/tcp6"].some((f) => {
      try {
        // local_address is ADDR:PORT in hex; state 0A is LISTEN.
        return readFileSync(f, "utf8").split("\n").some((l) => {
          const cols = l.trim().split(/\s+/);
          return cols[1]?.endsWith(`:${hex}`) && cols[3] === "0A";
        });
      } catch {
        return false;
      }
    });
  }
  try {
    return execFileSync("lsof", ["-nP", `-iTCP:${port}`, "-sTCP:LISTEN", "-t"], { encoding: "utf8" }).trim() !== "";
  } catch {
    return false; // lsof exits 1 when nothing matches
  }
}

/**
 * The local RTMP sink: one publisher, then it exits. -progress reports the media it has received. Returned
 * once it is listening, so the destination's first connection cannot beat it: with one restart allowed, a
 * sink that is late twice would read as a failure the test did not cause.
 */
async function startSink(): Promise<Child> {
  const sink = start([
    "-hide_banner", "-nostats", "-loglevel", "warning",
    "-listen", "1", "-i", SINK_URL,
    "-progress", "pipe:1", "-c", "copy", "-f", "null", "-",
  ]);
  await until(`the sink to listen on ${SINK_PORT}`, 15_000, () => sink.exited || listening(SINK_PORT), () => sink.out.slice(-300));
  if (sink.exited) throw new Error(`the sink exited before it listened: ${sink.out.slice(-300)}`);
  return sink;
}

/** Microseconds of media the sink has received, from its -progress lines. */
function sinkReceivedUs(sink: Child): number {
  const all = [...sink.out.matchAll(/out_time_us=(\d+)/g)];
  return all.length ? Number(all.at(-1)![1]) : 0;
}

/** A transport that records every request the runtime sends. */
function recording(): { make: (ca?: string) => Transport; sent: TransportRequest[] } {
  const sent: TransportRequest[] = [];
  return {
    sent,
    make: (ca) => {
      const inner = nodeTransport(ca);
      return {
        send(req) {
          sent.push(req);
          return inner.send(req);
        },
      };
    },
  };
}

async function api(path: string): Promise<unknown> {
  const res = await fetch(`${URL_}/api/v1${path}`, { headers: { Authorization: `Bearer ${ADMIN}` } });
  if (!res.ok) throw new Error(`GET ${path}: ${res.status} ${await res.text()}`);
  return res.json();
}

let feed: Child | undefined;

beforeAll(async () => {
  // The programme's relay hub port, once its engine is up: where the synthetic feed is sent, as the
  // acceptance suites send theirs.
  let relay = 0;
  await until("the programme's relay port", 30_000, async () => {
    const stats = (await api(`/stats?source=${SOURCE}`)) as { relay?: { port?: number } };
    relay = stats.relay?.port ?? 0;
    return relay > 0;
  });
  feed = start([
    "-hide_banner", "-loglevel", "error", "-re",
    "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30",
    "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
    "-map", "0:v", "-map", "1:a",
    "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-g", "30", "-b:v", "800k",
    "-c:a", "aac", "-b:a", "128k", "-metadata", `comment=${MARKER}`,
    "-f", "mpegts", "-flush_packets", "1", `udp://127.0.0.1:${relay}?pkt_size=1316`,
  ]);
  await until("the engine to measure the feed's layout", 60_000, async () => {
    const src = (await api(`/source?source=${SOURCE}`)) as { probed?: boolean; tracks?: unknown[] };
    return src.probed === true && (src.tracks?.length ?? 0) > 0;
  });
});

afterAll(async () => {
  for (const c of children) await stop(c, "a child of the e2e test");
});

describe("a Stream Deck paired with a real polyemesis", () => {
  const net = recording();
  const sched = manualScheduler();
  const rt = new Runtime("en", net.make, sched.schedule);
  const key = surface("dest", "destination", { destinationId: DEST, sourceId: SOURCE });
  const stopKey = surface("stop", "stopOne", { destinationId: DEST, sourceId: SOURCE });
  const meter = surface("meter", "meter", { sourceId: SOURCE });
  const mute = surface("mute", "trackMute", { destinationId: DEST, track: 0, sourceId: SOURCE });
  let sink: Child;

  const face = () => `${key.states.at(-1)} ${JSON.stringify(key.titles.at(-1))}`;
  const waitFace = (title: RegExp, state: number, ms = 45_000) =>
    until(`the destination key to read ${title} (state ${state})`, ms, async () => {
      await rt.tick();
      return key.states.at(-1) === state && title.test(key.titles.at(-1) ?? "");
    }, face);

  it("pairs: the connection check names an admin token", async () => {
    rt.configure({ url: URL_, token: ADMIN });
    const info = await rt.verify();
    expect(info).toMatchObject({ name: "streamdeck-e2e", scope: "admin", adminScope: true });
    expect(rt.isAdmin).toBe(true);
  });

  it("reads the state: the destination is off, its track unmuted, the meter live", async () => {
    for (const s of [key, stopKey, meter, mute]) rt.add(s);
    await waitFace(/^Off$/, KEY_STATE.idle);
    await until("the track mute key to read the stored level", 15_000, async () => {
      await rt.tick();
      return /^Unmuted/.test(mute.titles.at(-1) ?? "");
    }, () => JSON.stringify(mute.titles.at(-1)));
    await until("the meter dial to read a live level", 15_000, async () => {
      await rt.tickMeters();
      return /^-?\d+(\.\d)? dB$/.test(meter.feedback.at(-1)?.value ?? "");
    }, () => JSON.stringify(meter.feedback.at(-1)));
    const paths = new Set(net.sent.map((r) => `${r.method} ${r.url.pathname}`));
    for (const p of ["GET /api/v1/status", "GET /api/v1/destinations", "GET /api/v1/levels"]) expect(paths).toContain(p);
  });

  it("starts the destination from its key, and it goes live at the sink", async () => {
    sink = await startSink();
    await rt.pressDestination("dest");
    expect(net.sent.at(-1)?.url.pathname).toMatch(new RegExp(`/destinations/${DEST}/(start|restart)$`));
    await waitFace(/^Live$/, KEY_STATE.live);
    await until("the sink to receive a second of media", 30_000, () => sinkReceivedUs(sink) > 1_000_000, () => sink.out.slice(-300));
  });

  it("shows Failed with its reason when the sink goes away", async () => {
    await stop(sink, "the sink");
    // The output breaks, the one restart it is allowed finds nothing listening, and it gives up.
    await waitFace(/^Failed\nRefused$/, KEY_STATE.error);
  });

  it("retries from a tap on the Failed key, and is live again", async () => {
    sink = await startSink();
    const before = net.sent.length;
    await rt.pressDestination("dest");
    const writes = net.sent.slice(before).filter((r) => r.method === "POST");
    expect(writes.map((r) => r.url.pathname)).toEqual([`/api/v1/destinations/${DEST}/restart`]);
    expect(writes[0]!.url.searchParams.get("source")).toBe(String(SOURCE));
    await waitFace(/^Live$/, KEY_STATE.live);
    await until("the sink to receive media after the retry", 30_000, () => sinkReceivedUs(sink) > 1_000_000, () => sink.out.slice(-300));
  });

  it("stops it from a held stop key, and the sink sees the publisher leave", async () => {
    await rt.tick();
    await rt.pressStopOneDown("stop");
    sched.fire(HOLD_MS);
    await until("the stop to be sent", 10_000, () =>
      net.sent.some((r) => r.method === "POST" && r.url.pathname === `/api/v1/destinations/${DEST}/stop`),
    );
    await waitFace(/^Off$/, KEY_STATE.idle);
    await until("the sink to see the publisher leave", 15_000, () => sink.exited, () => sink.out.slice(-300));
  });
});

describe("a Stream Deck paired with a read token", () => {
  it("shows state, and a tap that would start the destination sends nothing and shows Needs admin", async () => {
    const net = recording();
    const rt = new Runtime("en", net.make, manualScheduler().schedule);
    rt.configure({ url: URL_, token: READ });
    expect(await rt.verify()).toMatchObject({ scope: "read", adminScope: false });
    const key = surface("dest", "destination", { destinationId: DEST, sourceId: SOURCE });
    rt.add(key);
    await rt.tick();
    expect(key.titles.at(-1)).toBe("Off");
    await rt.pressDestination("dest");
    // Method and path only: a request object carries the token, and a failing diff would print it.
    expect(net.sent.filter((r) => r.method === "POST").map((r) => r.url.pathname)).toEqual([]);
    expect(key.states.at(-1)).toBe(KEY_STATE.needsAdmin);
  });
});
