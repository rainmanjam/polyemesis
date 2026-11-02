import { describe, expect, it } from "vitest";
import type { TransportRequest } from "../src/api.js";
import { dumpSeconds, keySettings } from "../src/config.js";
import type { StatusPayload } from "../src/model.js";
import { KEY_STATE } from "../src/render.js";
import { FLASH_MS, HOLD_MS } from "../src/runtime.js";
import { ADMIN, PAIRED, READ, json, runtimeFor, settle, surface, type Route } from "./fakeInstall.js";

// The broadcast delay on the deck: the stop key's "cut the delayed tail" option and the dump key.
// Both throw programme away for good, so both confirm by a held press: a tap, or a release before
// HOLD_MS, sends nothing. What a hold acts on is read at key DOWN, so an inspector edit during the
// hold cannot widen it (a destination dump into a dump of every destination, a plain stop into a cut).

const STATUS: StatusPayload = {
  source: { id: 7 },
  destinations: [
    {
      id: 5, name: "Twitch", platform: "twitch", enabled: true, sourceId: 7, process: { state: "running" },
      delay: { seconds: 30, phase: "on-air", effectiveSeconds: 18, dumpedSeconds: 12, dumpable: true },
    },
    {
      id: 6, name: "YouTube", platform: "youtube", enabled: false, sourceId: 7, process: null,
      delay: { seconds: 30, phase: "draining", drainRemainingSeconds: 41.2, effectiveSeconds: 30, dumpable: false },
    },
    {
      id: 8, name: "Kick", platform: "kick", enabled: true, sourceId: 7, process: { state: "stopped" },
      delay: { seconds: 20, phase: "buffering", effectiveSeconds: 20, dumpable: false },
    },
    {
      id: 10, name: "Facebook", platform: "facebook", enabled: true, sourceId: 7, process: { state: "running" },
      delay: { seconds: 30, phase: "on-air", effectiveSeconds: 30, dumpable: true },
    },
    { id: 4, name: "Plain", platform: "twitch", enabled: true, sourceId: 7, process: { state: "running" } },
    {
      id: 9, name: "Other", platform: "twitch", enabled: true, sourceId: 9, process: { state: "running" },
      delay: { seconds: 30, phase: "on-air", effectiveSeconds: 30, dumpable: true },
    },
  ],
};

const DUMPED: Route = json(200, {
  id: 5, name: "Twitch", complete: true, requestedSeconds: 10, dumpedSeconds: 11.6, clamped: false,
  delayBeforeSeconds: 30, delayAfterSeconds: 18.4, configuredSeconds: 30, restoresOnNextStart: true,
});

const ALL_DUMPED: Route = json(200, {
  action: "dump",
  results: [
    { id: 5, name: "Twitch", outcome: "dumped", dumpedSeconds: 11 },
    { id: 10, name: "Facebook", outcome: "dumped", dumpedSeconds: 12 },
    { id: 8, name: "Kick", outcome: "skipped", message: "still filling" },
  ],
});

async function deck(token: Route, extra: Record<string, Route> = {}) {
  // Routes are looked up at send time, so a test can change one after the first tick.
  const routes: Record<string, Route> = {
    "GET /api/v1/auth/connection": token,
    "GET /api/v1/status": json(200, STATUS),
    "GET /api/v1/destinations/stop-impact": json(200, { destinations: 1, youtubeBroadcasts: 0 }),
    "POST /api/v1/destinations/5/delay/dump": DUMPED,
    "POST /api/v1/destinations/delay/dump-all": ALL_DUMPED,
    "POST /api/v1/destinations/5/stop": json(200, {}),
    "POST /api/v1/destinations/6/stop": json(200, {}),
    ...extra,
  };
  const env = runtimeFor(routes);
  env.rt.configure(PAIRED);
  await env.rt.verify();
  return { ...env, routes };
}

/** A route that answers only when the test says so: a request still in flight. */
function deferred(answer: Route) {
  let release: () => void = () => {};
  const gate = new Promise<void>((r) => (release = r));
  const route = ((req: TransportRequest) => gate.then(() => answer(req))) as unknown as Route;
  return { route, release };
}

/** STATUS with only these destinations dumpable now. */
function statusDumpable(ids: number[]): StatusPayload {
  return {
    ...STATUS,
    destinations: STATUS.destinations!.map((d) =>
      d.delay ? { ...d, delay: { ...d.delay, dumpable: ids.includes(d.id) } } : d,
    ),
  };
}

const body = (req: { body?: string }) => (req.body === undefined ? undefined : JSON.parse(req.body));

describe("dump key: hold to confirm", () => {
  it("sends nothing on key down, and nothing at all when released before the hold completes", async () => {
    const { rt, net, sched } = await deck(ADMIN);
    rt.add(surface("k", "dump", { sourceId: 7, destinationId: 5 }));
    await rt.tick();

    await rt.pressDumpDown("k");
    expect(net.writes()).toHaveLength(0);
    expect(sched.pending(HOLD_MS)).toBe(1);
    await rt.pressHoldUp("k");
    sched.fireAll();
    await settle();
    expect(net.writes()).toHaveLength(0);
  });

  it("only a full hold sends the dump, to the destination's own route, with the key's seconds", async () => {
    const { rt, net, sched } = await deck(ADMIN);
    rt.add(surface("k", "dump", { sourceId: 7, destinationId: 5, dumpSeconds: 10 }));
    await rt.tick();

    await rt.pressDumpDown("k");
    expect(net.writes()).toHaveLength(0);
    sched.fire(HOLD_MS);
    await settle();
    const writes = net.writes();
    expect(writes.map((r) => r.url.pathname)).toEqual(["/api/v1/destinations/5/delay/dump"]);
    expect(body(writes[0]!)).toEqual({ seconds: 10 });
  });

  it("sends no body when the key has no seconds, which dumps everything since the last dump point", async () => {
    const { rt, net, sched } = await deck(ADMIN);
    rt.add(surface("k", "dump", { sourceId: 7, destinationId: 5 }));
    await rt.tick();

    await rt.pressDumpDown("k");
    sched.fire(HOLD_MS);
    await settle();
    expect(net.writes()).toHaveLength(1);
    expect(net.writes()[0]!.body).toBeUndefined();
  });

  it("with no destination dumps every delayed destination of the programme, confirmed, by dump-all", async () => {
    const { rt, net, sched } = await deck(ADMIN);
    rt.add(surface("a", "dump", { sourceId: 7 }));
    rt.add(surface("b", "dump", { sourceId: 7, dumpSeconds: 5 }));
    await rt.tick();

    await rt.pressDumpDown("a");
    await rt.pressDumpDown("b");
    expect(net.writes()).toHaveLength(0);
    sched.fire(HOLD_MS);
    await settle();
    const [a, b] = net.writes();
    expect(a!.url.pathname).toBe("/api/v1/destinations/delay/dump-all");
    expect(a!.url.searchParams.get("source")).toBe("7");
    expect(body(a!)).toEqual({ confirm: true });
    expect(body(b!)).toEqual({ confirm: true, seconds: 5 });
  });

  it("dumps the destination and seconds armed at key down, whatever the settings say when the hold completes", async () => {
    const { rt, net, sched } = await deck(ADMIN);
    const key = surface("k", "dump", { sourceId: 7, destinationId: 5, dumpSeconds: 10 });
    rt.add(key);
    await rt.tick();

    await rt.pressDumpDown("k");
    // Cleared mid-hold: must NOT widen into a dump of every destination.
    key.settings.destinationId = undefined;
    key.settings.dumpSeconds = 900;
    sched.fire(HOLD_MS);
    await settle();
    expect(net.writes().map((r) => r.url.pathname)).toEqual(["/api/v1/destinations/5/delay/dump"]);
    expect(body(net.writes()[0]!)).toEqual({ seconds: 10 });
  });

  it("stays a dump of all when a destination is set mid-hold", async () => {
    const { rt, net, sched } = await deck(ADMIN);
    const key = surface("k", "dump", { sourceId: 7 });
    rt.add(key);
    await rt.tick();

    await rt.pressDumpDown("k");
    key.settings.destinationId = 5;
    key.settings.dumpSeconds = 10;
    sched.fire(HOLD_MS);
    await settle();
    expect(net.writes().map((r) => r.url.pathname)).toEqual(["/api/v1/destinations/delay/dump-all"]);
    expect(body(net.writes()[0]!)).toEqual({ confirm: true });
  });

  it("refuses a destination on another programme, and one it has not seen, sending nothing", async () => {
    const { rt, net, sched } = await deck(ADMIN);
    const other = surface("o", "dump", { sourceId: 7, destinationId: 9 });
    const gone = surface("g", "dump", { sourceId: 7, destinationId: 99 });
    rt.add(other);
    rt.add(gone);
    await rt.tick();
    expect(other.states.at(-1)).toBe(KEY_STATE.unbound);
    expect(other.titles.at(-1)).toBe("Other programme");
    expect(gone.states.at(-1)).toBe(KEY_STATE.unbound);

    await rt.pressDumpDown("o");
    await rt.pressDumpDown("g");
    expect(other.alerts).toBe(1);
    expect(gone.alerts).toBe(1);
    expect(sched.pending(HOLD_MS)).toBe(0);
    sched.fireAll();
    await settle();
    expect(net.writes()).toHaveLength(0);
  });

  it("refuses seconds outside 1 to 900 instead of reading them as everything since the last dump", async () => {
    const { rt, net, sched } = await deck(ADMIN);
    const key = surface("k", "dump", { sourceId: 7, destinationId: 5, dumpSeconds: 1000 });
    rt.add(key);
    await rt.tick();
    expect(key.titles.at(-1)).toBe("Seconds: 1–900");

    await rt.pressDumpDown("k");
    expect(key.alerts).toBe(1);
    expect(sched.pending(HOLD_MS)).toBe(0);
    sched.fireAll();
    await settle();
    expect(net.writes()).toHaveLength(0);
  });

  it("cannot dump from a read-only token: no request, and the key says it needs admin", async () => {
    const { rt, net, sched } = await deck(READ);
    const one = surface("k", "dump", { sourceId: 7, destinationId: 5 });
    const all = surface("a", "dump", { sourceId: 7 });
    rt.add(one);
    rt.add(all);
    await rt.tick();
    expect(one.states.at(-1)).toBe(KEY_STATE.needsAdmin);
    expect(all.states.at(-1)).toBe(KEY_STATE.needsAdmin);

    await rt.pressDumpDown("k");
    await rt.pressDumpDown("a");
    expect(one.titles.at(-1)).toBe("Needs admin token");
    expect(sched.pending(HOLD_MS)).toBe(0);
    sched.fireAll();
    await settle();
    expect(net.writes()).toHaveLength(0);
  });
});

describe("dump key face", () => {
  it("shows the delay as it runs after a dump, against the configured delay", async () => {
    const { rt } = await deck(ADMIN);
    const key = surface("k", "dump", { sourceId: 7, destinationId: 5 });
    rt.add(key);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.idle);
    expect(key.titles.at(-1)).toBe("Hold to dump\nTwitch\nDelay 18/30s");
  });

  it("shows the configured delay alone when nothing has been dumped", async () => {
    const { rt } = await deck(ADMIN);
    const key = surface("k", "dump", { sourceId: 7, destinationId: 10 });
    rt.add(key);
    await rt.tick();
    expect(key.titles.at(-1)).toBe("Hold to dump\nFacebook\nDelay 30s");
  });

  it("is not ready while the delay fills, while the tail airs, or with no delay at all", async () => {
    const { rt } = await deck(ADMIN);
    const filling = surface("f", "dump", { sourceId: 7, destinationId: 8 });
    const draining = surface("d", "dump", { sourceId: 7, destinationId: 6 });
    const plain = surface("p", "dump", { sourceId: 7, destinationId: 4 });
    for (const k of [filling, draining, plain]) rt.add(k);
    await rt.tick();
    for (const k of [filling, draining, plain]) expect(k.states.at(-1), k.id).toBe(KEY_STATE.unavailable);
    expect(filling.titles.at(-1)).toBe("Buffering delay\nKick\nDelay 20s");
    expect(draining.titles.at(-1)).toMatch(/^Airing delay tail\nYouTube/);
    expect(plain.titles.at(-1)).toBe("No delay\nPlain");
  });

  it("with no destination counts the programme's delayed destinations a hold would dump", async () => {
    const { rt } = await deck(ADMIN);
    const key = surface("a", "dump", { sourceId: 7 });
    rt.add(key);
    await rt.tick();
    // 5 and 10 are dumpable on programme 7; 9 is on programme 9, 6 drains, 8 fills, 4 has no delay.
    expect(key.titles.at(-1)).toBe("Hold to dump all\n2 delayed");
  });

  it("shows what the hold will drop while it is held", async () => {
    const { rt } = await deck(ADMIN);
    const key = surface("k", "dump", { sourceId: 7, destinationId: 5, dumpSeconds: 10 });
    rt.add(key);
    await rt.tick();
    await rt.pressDumpDown("k");
    expect(key.states.at(-1)).toBe(KEY_STATE.held);
    expect(key.titles.at(-1)).toBe("Hold to dump\nLast 10s");
  });
});

describe("dump key flash", () => {
  it("flashes what the server dropped, through the key up and a tick, then returns to its face", async () => {
    const { rt, sched } = await deck(ADMIN);
    const key = surface("k", "dump", { sourceId: 7, destinationId: 5, dumpSeconds: 10 });
    rt.add(key);
    await rt.tick();

    await rt.pressDumpDown("k");
    sched.fire(HOLD_MS);
    await settle();
    expect(key.oks).toBe(1);
    // 11.6 s answered: the face says what went, rounded, not what was asked for.
    expect(key.titles.at(-1)).toBe("Dumped 12s");
    expect(key.states.at(-1)).toBe(KEY_STATE.live);
    expect(sched.pending(FLASH_MS)).toBe(1);

    await rt.pressHoldUp("k");
    await rt.tick();
    expect(key.titles.at(-1)).toBe("Dumped 12s");

    sched.fire(FLASH_MS);
    await settle();
    expect(key.titles.at(-1)).toBe("Hold to dump\nTwitch\nDelay 18/30s");
  });

  it("a new press during the flash ends it, so an early release repaints the key's own face", async () => {
    const { rt, net, sched } = await deck(ADMIN);
    const key = surface("k", "dump", { sourceId: 7, destinationId: 5 });
    rt.add(key);
    await rt.tick();
    await rt.pressDumpDown("k");
    sched.fire(HOLD_MS);
    await settle();
    await rt.pressHoldUp("k");
    expect(key.titles.at(-1)).toBe("Dumped 12s");

    await rt.pressDumpDown("k");
    expect(key.states.at(-1)).toBe(KEY_STATE.held);
    await rt.pressHoldUp("k");
    expect(key.states.at(-1)).toBe(KEY_STATE.idle);
    expect(key.titles.at(-1)).toBe("Hold to dump\nTwitch\nDelay 18/30s");
    sched.fireAll();
    await settle();
    expect(net.writes()).toHaveLength(1);
  });

  it("alerts and keeps its face when the server has nothing to dump", async () => {
    const { rt, sched } = await deck(ADMIN, {
      "POST /api/v1/destinations/5/delay/dump": json(409, { error: "nothing to dump" }),
    });
    const key = surface("k", "dump", { sourceId: 7, destinationId: 5 });
    rt.add(key);
    await rt.tick();

    await rt.pressDumpDown("k");
    sched.fire(HOLD_MS);
    await settle();
    expect(key.alerts).toBe(1);
    expect(key.oks).toBe(0);
    expect(sched.pending(FLASH_MS)).toBe(0);
    expect(key.titles.some((t) => t.startsWith("Dumped"))).toBe(false);
    await rt.pressHoldUp("k");
    expect(key.titles.at(-1)).toBe("Hold to dump\nTwitch\nDelay 18/30s");
  });

  it("flashes how many destinations a dump of all dropped", async () => {
    const { rt, sched } = await deck(ADMIN);
    const key = surface("a", "dump", { sourceId: 7 });
    rt.add(key);
    await rt.tick();
    await rt.pressDumpDown("a");
    sched.fire(HOLD_MS);
    await settle();
    expect(key.titles.at(-1)).toBe("2 dumped");
    expect(key.oks).toBe(1);
    expect(key.alerts).toBe(0);
  });

  it("alerts on a dump of all where one failed, and on one where nothing was dumped", async () => {
    const partial = await deck(ADMIN, {
      "POST /api/v1/destinations/delay/dump-all": json(200, {
        action: "dump",
        results: [
          { id: 5, name: "Twitch", outcome: "dumped", dumpedSeconds: 11 },
          { id: 10, name: "Facebook", outcome: "failed", message: "no keyframe" },
        ],
      }),
    });
    const a = surface("a", "dump", { sourceId: 7 });
    partial.rt.add(a);
    await partial.rt.tick();
    await partial.rt.pressDumpDown("a");
    partial.sched.fire(HOLD_MS);
    await settle();
    expect(a.alerts).toBe(1);
    expect(a.titles.at(-1)).toBe("1 dumped");

    const none = await deck(ADMIN, {
      "POST /api/v1/destinations/delay/dump-all": json(200, {
        action: "dump",
        results: [{ id: 5, name: "Twitch", outcome: "skipped", message: "nothing to dump" }],
      }),
    });
    const b = surface("b", "dump", { sourceId: 7 });
    none.rt.add(b);
    await none.rt.tick();
    await none.rt.pressDumpDown("b");
    none.sched.fire(HOLD_MS);
    await settle();
    expect(b.alerts).toBe(1);
    expect(none.sched.pending(FLASH_MS)).toBe(0);
  });
});

// A dump of all with no programme is a dump across every programme on the install. The key never asks
// for that: on a multi-programme install it must name a programme, and what it counts is read fresh at
// key down, so a failed or stale read can never arm it.
describe("dump of all: scoped and counted fresh at key down", () => {
  it("refuses to arm with no programme on an install with more than one, sending nothing", async () => {
    const { rt, net, sched } = await deck(ADMIN, {
      "GET /api/v1/sources": json(200, [{ id: 7 }, { id: 9 }]),
    });
    const key = surface("a", "dump", {});
    rt.add(key);
    await rt.tick();

    await rt.pressDumpDown("a");
    expect(key.alerts).toBe(1);
    expect(key.titles.at(-1)).toBe("Set a programme");
    expect(sched.pending(HOLD_MS)).toBe(0);
    sched.fireAll();
    await settle();
    expect(net.writes()).toHaveLength(0);
  });

  it("refuses to arm with no programme when the programme list cannot be read", async () => {
    const { rt, net, sched } = await deck(ADMIN, {
      "GET /api/v1/sources": json(500, { error: "store down" }),
    });
    const key = surface("a", "dump", {});
    rt.add(key);
    await rt.tick();

    await rt.pressDumpDown("a");
    expect(key.alerts).toBe(1);
    expect(sched.pending(HOLD_MS)).toBe(0);
    sched.fireAll();
    await settle();
    expect(net.writes()).toHaveLength(0);
  });

  it("arms with no programme on a single-programme install, and sends neither ?source= nor allProgrammes", async () => {
    const { rt, net, sched } = await deck(ADMIN, {
      "GET /api/v1/sources": json(200, [{ id: 7 }]),
    });
    rt.add(surface("a", "dump", {}));
    await rt.tick();

    await rt.pressDumpDown("a");
    expect(sched.pending(HOLD_MS)).toBe(1);
    sched.fire(HOLD_MS);
    await settle();
    const writes = net.writes();
    expect(writes.map((r) => r.url.pathname)).toEqual(["/api/v1/destinations/delay/dump-all"]);
    expect(writes[0]!.url.searchParams.has("source")).toBe(false);
    expect(body(writes[0]!)).toEqual({ confirm: true });
  });

  it("does not read the programme list when the key names a programme", async () => {
    const { rt, net } = await deck(ADMIN, {
      "GET /api/v1/sources": json(200, [{ id: 7 }, { id: 9 }]),
    });
    rt.add(surface("a", "dump", { sourceId: 7 }));
    await rt.tick();
    await rt.pressDumpDown("a");
    expect(net.sent.some((r) => r.url.pathname === "/api/v1/sources")).toBe(false);
  });

  it("reads the status fresh at key down, scoped to the key's programme, and shows that count while held", async () => {
    const { rt, net, routes } = await deck(ADMIN);
    const key = surface("a", "dump", { sourceId: 7 });
    rt.add(key);
    await rt.tick();
    expect(key.titles.at(-1)).toBe("Hold to dump all\n2 delayed");

    // Between the last tick and the press, Facebook stopped being dumpable.
    routes["GET /api/v1/status"] = json(200, statusDumpable([5]));
    const before = net.sent.length;
    await rt.pressDumpDown("a");
    const read = net.sent.slice(before).find((r) => r.url.pathname === "/api/v1/status");
    expect(read?.url.searchParams.get("source")).toBe("7");
    expect(key.states.at(-1)).toBe(KEY_STATE.held);
    expect(key.titles.at(-1)).toBe("Hold to dump all\n1 delayed\nSince last dump");
  });

  it("refuses to arm when the fresh status read fails, whatever the last tick showed", async () => {
    const { rt, net, sched, routes } = await deck(ADMIN);
    const key = surface("a", "dump", { sourceId: 7 });
    rt.add(key);
    await rt.tick();

    routes["GET /api/v1/status"] = json(500, { error: "boom" });
    await rt.pressDumpDown("a");
    expect(key.alerts).toBe(1);
    expect(sched.pending(HOLD_MS)).toBe(0);
    sched.fireAll();
    await settle();
    expect(net.writes()).toHaveLength(0);
  });

  it("refuses to arm when nothing on the programme is dumpable now", async () => {
    const { rt, net, sched, routes } = await deck(ADMIN);
    const key = surface("a", "dump", { sourceId: 7 });
    rt.add(key);
    await rt.tick();

    // Only programme 9's destination is dumpable: it must not count for programme 7.
    routes["GET /api/v1/status"] = json(200, statusDumpable([9]));
    await rt.pressDumpDown("a");
    expect(key.alerts).toBe(1);
    expect(sched.pending(HOLD_MS)).toBe(0);
    sched.fireAll();
    await settle();
    expect(net.writes()).toHaveLength(0);
  });

  it("does not arm when the key is let go while the programme list is still loading", async () => {
    const slow = deferred(json(200, [{ id: 7 }]));
    const { rt, net, sched } = await deck(ADMIN, { "GET /api/v1/sources": slow.route });
    rt.add(surface("a", "dump", {}));
    await rt.tick();

    const pressing = rt.pressDumpDown("a");
    await settle();
    await rt.pressHoldUp("a");
    slow.release();
    await pressing;
    expect(sched.pending(HOLD_MS)).toBe(0);
    sched.fireAll();
    await settle();
    expect(net.writes()).toHaveLength(0);
  });
});

// The server holds a dump's answer until the stream resumes at the next keyframe, up to 12 s. A
// transport that gave up first would make a dump that went through look failed, and the operator would
// hold again; so the two dump calls wait longer than that, and the key says the dump is under way.
describe("dump in flight", () => {
  it("gives the two dump calls a timeout above the server's 12 s keyframe wait, and no other call", async () => {
    const { rt, net, sched } = await deck(ADMIN);
    rt.add(surface("k", "dump", { sourceId: 7, destinationId: 5 }));
    rt.add(surface("a", "dump", { sourceId: 7 }));
    rt.add(surface("s", "stopOne", { sourceId: 7, destinationId: 5 }));
    await rt.tick();
    await rt.pressDumpDown("k");
    await rt.pressDumpDown("a");
    await rt.pressStopOneDown("s");
    sched.fire(HOLD_MS);
    await settle();

    const dumps = net.sent.filter((r) => r.url.pathname.includes("/delay/dump"));
    expect(dumps.map((r) => r.url.pathname).sort()).toEqual([
      "/api/v1/destinations/5/delay/dump",
      "/api/v1/destinations/delay/dump-all",
    ]);
    for (const r of dumps) expect(r.timeoutMs, r.url.pathname).toBeGreaterThan(12_000);
    const others = net.sent.filter((r) => !r.url.pathname.includes("/delay/dump"));
    expect(others.map((r) => r.url.pathname)).toContain("/api/v1/destinations/5/stop");
    for (const r of others) expect(r.timeoutMs, r.url.pathname).toBeUndefined();
  });

  it("shows Dumping… from the confirm until the answer, through a key up and a tick", async () => {
    const slow = deferred(DUMPED);
    const { rt, sched } = await deck(ADMIN, { "POST /api/v1/destinations/5/delay/dump": slow.route });
    const key = surface("k", "dump", { sourceId: 7, destinationId: 5 });
    rt.add(key);
    await rt.tick();

    await rt.pressDumpDown("k");
    sched.fire(HOLD_MS);
    await settle();
    expect(key.titles.at(-1)).toBe("Dumping…");
    await rt.pressHoldUp("k");
    await rt.tick();
    expect(key.titles.at(-1)).toBe("Dumping…");

    slow.release();
    await settle();
    expect(key.titles.at(-1)).toBe("Dumped 12s");
  });

  it("shows Dumping… on a dump of all too", async () => {
    const slow = deferred(ALL_DUMPED);
    const { rt, sched } = await deck(ADMIN, { "POST /api/v1/destinations/delay/dump-all": slow.route });
    const key = surface("a", "dump", { sourceId: 7 });
    rt.add(key);
    await rt.tick();
    await rt.pressDumpDown("a");
    sched.fire(HOLD_MS);
    await settle();
    expect(key.titles.at(-1)).toBe("Dumping…");
    await rt.pressHoldUp("a");
    expect(key.titles.at(-1)).toBe("Dumping…");
    slow.release();
    await settle();
    expect(key.titles.at(-1)).toBe("2 dumped");
  });

  it("a second hold while a dump is in flight does nothing", async () => {
    const slow = deferred(DUMPED);
    const { rt, net, sched } = await deck(ADMIN, { "POST /api/v1/destinations/5/delay/dump": slow.route });
    const key = surface("k", "dump", { sourceId: 7, destinationId: 5 });
    rt.add(key);
    await rt.tick();
    await rt.pressDumpDown("k");
    sched.fire(HOLD_MS);
    await settle();
    await rt.pressHoldUp("k");

    const before = net.sent.length;
    await rt.pressDumpDown("k");
    expect(net.sent.length).toBe(before);
    expect(sched.pending(HOLD_MS)).toBe(0);
    expect(key.alerts).toBe(0);
    expect(key.titles.at(-1)).toBe("Dumping…");
    sched.fireAll();
    await settle();
    expect(net.writes()).toHaveLength(1);

    slow.release();
    await settle();
    expect(net.writes()).toHaveLength(1);
  });

  it("puts its own face back as soon as a refusal answers, when the key was let go mid-dump", async () => {
    const slow = deferred(json(409, { error: "nothing to dump" }));
    const { rt, sched } = await deck(ADMIN, { "POST /api/v1/destinations/5/delay/dump": slow.route });
    const key = surface("k", "dump", { sourceId: 7, destinationId: 5 });
    rt.add(key);
    await rt.tick();
    await rt.pressDumpDown("k");
    sched.fire(HOLD_MS);
    await settle();
    await rt.pressHoldUp("k");
    expect(key.titles.at(-1)).toBe("Dumping…");

    slow.release();
    await settle();
    expect(key.alerts).toBe(1);
    expect(key.titles.at(-1)).toBe("Hold to dump\nTwitch\nDelay 18/30s");
  });

  it("arms again once a refused dump has answered", async () => {
    const { rt, net, sched } = await deck(ADMIN, {
      "POST /api/v1/destinations/5/delay/dump": json(409, { error: "nothing to dump" }),
    });
    const key = surface("k", "dump", { sourceId: 7, destinationId: 5 });
    rt.add(key);
    await rt.tick();
    await rt.pressDumpDown("k");
    sched.fire(HOLD_MS);
    await settle();
    await rt.pressHoldUp("k");
    expect(key.alerts).toBe(1);

    await rt.pressDumpDown("k");
    expect(sched.pending(HOLD_MS)).toBe(1);
    sched.fire(HOLD_MS);
    await settle();
    expect(net.writes()).toHaveLength(2);
  });
});

describe("stop key: cut the delayed tail", () => {
  it("cuts only after the full hold: a tap sends nothing, a hold sends the stop with now=true", async () => {
    const { rt, net, sched } = await deck(ADMIN);
    rt.add(surface("k", "stopOne", { sourceId: 7, destinationId: 6, cutTail: true }));
    await rt.tick();

    await rt.pressStopOneDown("k");
    expect(net.writes()).toHaveLength(0);
    await rt.pressHoldUp("k");
    sched.fireAll();
    await settle();
    expect(net.writes()).toHaveLength(0);

    await rt.pressStopOneDown("k");
    expect(net.writes()).toHaveLength(0);
    sched.fire(HOLD_MS);
    await settle();
    const writes = net.writes();
    expect(writes.map((r) => r.url.pathname)).toEqual(["/api/v1/destinations/6/stop"]);
    expect(writes[0]!.url.searchParams.get("now")).toBe("true");
  });

  it("sends a plain stop, no now, when the option is off", async () => {
    const { rt, net, sched } = await deck(ADMIN);
    rt.add(surface("k", "stopOne", { sourceId: 7, destinationId: 5 }));
    await rt.tick();
    await rt.pressStopOneDown("k");
    sched.fire(HOLD_MS);
    await settle();
    expect(net.writes()).toHaveLength(1);
    expect(net.writes()[0]!.url.searchParams.has("now")).toBe(false);
  });

  it("acts on the cut flag armed at key down: ticking the box mid-hold does not cut", async () => {
    const { rt, net, sched } = await deck(ADMIN);
    const key = surface("k", "stopOne", { sourceId: 7, destinationId: 5 });
    rt.add(key);
    await rt.tick();
    await rt.pressStopOneDown("k");
    key.settings.cutTail = true;
    sched.fire(HOLD_MS);
    await settle();
    expect(net.writes()[0]!.url.searchParams.has("now")).toBe(false);
  });

  it("acts on the cut flag armed at key down: unticking the box mid-hold still cuts", async () => {
    const { rt, net, sched } = await deck(ADMIN);
    const key = surface("k", "stopOne", { sourceId: 7, destinationId: 6, cutTail: true });
    rt.add(key);
    await rt.tick();
    await rt.pressStopOneDown("k");
    key.settings.cutTail = undefined;
    sched.fire(HOLD_MS);
    await settle();
    expect(net.writes()[0]!.url.searchParams.get("now")).toBe("true");
  });

  it("shows the tail still airing on a draining destination, rounded up, and that a hold cuts it", async () => {
    const { rt } = await deck(ADMIN);
    const plain = surface("p", "stopOne", { sourceId: 7, destinationId: 6 });
    const cut = surface("c", "stopOne", { sourceId: 7, destinationId: 6, cutTail: true });
    rt.add(plain);
    rt.add(cut);
    await rt.tick();
    expect(plain.states.at(-1)).toBe(KEY_STATE.warn);
    expect(plain.titles.at(-1)).toBe("Tail 42s\nYouTube");
    expect(cut.titles.at(-1)).toBe("Tail 42s\nYouTube\nHold to cut");

    await rt.pressStopOneDown("c");
    expect(cut.states.at(-1)).toBe(KEY_STATE.held);
    expect(cut.titles.at(-1)).toMatch(/^Hold to cut\n/);
  });

  it("says the option is on while the destination is still live", async () => {
    const { rt } = await deck(ADMIN);
    const key = surface("k", "stopOne", { sourceId: 7, destinationId: 5, cutTail: true });
    rt.add(key);
    await rt.tick();
    expect(key.titles.at(-1)).toBe("Hold to stop\nTwitch\nAlso cuts tail");
  });
});

describe("delay key settings", () => {
  it("reads the cut option as true or absent, never false", () => {
    expect(keySettings({ cutTail: true }).cutTail).toBe(true);
    expect(keySettings({ cutTail: false }).cutTail).toBeUndefined();
    expect(keySettings({ cutTail: "yes" }).cutTail).toBeUndefined();
  });

  it("reads empty and 0 seconds as since-the-last-dump, and keeps a bad value for the key to refuse", () => {
    expect(dumpSeconds(undefined)).toBeUndefined();
    expect(dumpSeconds("")).toBeUndefined();
    expect(dumpSeconds(0)).toBeUndefined();
    expect(dumpSeconds("10")).toBe(10);
    expect(keySettings({ dumpSeconds: "1e" }).dumpSeconds).toBeNaN();
    expect(dumpSeconds(1000)).toBe(1000);
    expect(Number.isNaN(dumpSeconds("ten"))).toBe(true);
  });
});
