import { describe, expect, it } from "vitest";
import type { Transport, TransportRequest, TransportResponse } from "../src/api.js";
import { Runtime, type Surface, type SurfaceKind } from "../src/runtime.js";
import { KEY_STATE } from "../src/render.js";
import type { StatusPayload } from "../src/model.js";

type Route = (req: TransportRequest) => TransportResponse | Error;

/** A fake install. Routes are keyed "METHOD /api/v1/path"; anything unrouted is a 404. */
function install(routes: Record<string, Route>) {
  const sent: TransportRequest[] = [];
  const transport: Transport = {
    async send(req) {
      sent.push(req);
      const route = routes[`${req.method} ${req.url.pathname}`];
      const out = route ? route(req) : { status: 404, body: '{"error":"no route"}' };
      if (out instanceof Error) throw out;
      return out;
    },
  };
  return { transport, sent, writes: () => sent.filter((r) => r.method === "POST") };
}

const json = (status: number, body: unknown): Route => () => ({ status, body: JSON.stringify(body) });
const ADMIN = json(200, { name: "Stream Deck", prefix: "pmk", scope: "admin", adminScope: true });
const READ = json(200, { name: "Stream Deck", prefix: "pmk", scope: "read", adminScope: false });
const PAIRED = { url: "https://studio.example", token: "t" };

function surface(id: string, kind: SurfaceKind, settings: Surface["settings"] = {}) {
  const rec = {
    states: [] as number[],
    titles: [] as string[],
    alerts: 0,
    oks: 0,
    feedback: [] as { title: string; value: string; indicator: number }[],
  };
  const s: Surface & typeof rec = Object.assign(rec, {
    id,
    kind,
    settings,
    async setState(n: number) {
      rec.states.push(n);
    },
    async setTitle(title: string) {
      rec.titles.push(title);
    },
    async showAlert() {
      rec.alerts++;
    },
    async showOk() {
      rec.oks++;
    },
    async setFeedback(p: { title: string; value: string; indicator: number }) {
      rec.feedback.push(p);
    },
  });
  return s;
}

/** Timers the test fires by hand. */
function manualScheduler() {
  const timers: { fn: () => void; cancelled: boolean; ran: boolean }[] = [];
  return {
    schedule: (fn: () => void) => {
      const t = { fn, cancelled: false, ran: false };
      timers.push(t);
      return () => {
        t.cancelled = true;
      };
    },
    fireAll() {
      for (const t of timers) if (!t.cancelled && !t.ran) (t.ran = true), t.fn();
    },
    count: () => timers.filter((t) => !t.cancelled && !t.ran).length,
  };
}

function runtimeFor(routes: Record<string, Route>) {
  const net = install(routes);
  const sched = manualScheduler();
  const rt = new Runtime("en", () => net.transport, sched.schedule);
  return { rt, net, sched };
}

const STATUS_7: StatusPayload = {
  destinations: [
    { id: 5, name: "Twitch", platform: "twitch", enabled: true, sourceId: 7, process: { state: "running" } },
    { id: 6, name: "YouTube", platform: "youtube", enabled: false, sourceId: 7, process: null },
    { id: 9, name: "Other", platform: "twitch", enabled: true, sourceId: 9, process: { state: "running" } },
  ],
  failover: { active: "backup" },
};

describe("write keys on a read token", () => {
  it("sends no start-all, shows needs-admin, and does not make the request", async () => {
    const { rt, net } = runtimeFor({
      "GET /api/v1/auth/connection": READ,
      "GET /api/v1/status": json(200, STATUS_7),
    });
    rt.configure(PAIRED);
    await rt.verify();
    const go = surface("g", "goLive", { sourceId: 7 });
    rt.add(go);
    await rt.pressGoLive("g");
    expect(net.writes()).toHaveLength(0);
    expect(go.alerts).toBe(1);
    expect(go.titles.at(-1)).toBe("Needs admin token");
  });

  it("sends no stop request and never asks for an impact count on a stop key", async () => {
    const { rt, net } = runtimeFor({ "GET /api/v1/auth/connection": READ });
    rt.configure(PAIRED);
    await rt.verify();
    rt.add(surface("s", "stopAll", { sourceId: 7 }));
    await rt.pressStopDown("s");
    expect(net.sent.filter((r) => r.url.pathname.includes("/destinations/"))).toHaveLength(0);
  });
});

describe("go live", () => {
  it("starts the key's own programme with an admin token", async () => {
    const { rt, net } = runtimeFor({ "GET /api/v1/auth/connection": ADMIN, "POST /api/v1/destinations/start-all": json(200, {}) });
    rt.configure(PAIRED);
    await rt.verify();
    const go = surface("g", "goLive", { sourceId: 7 });
    rt.add(go);
    await rt.pressGoLive("g");
    const post = net.writes()[0]!;
    expect(post.url.pathname).toBe("/api/v1/destinations/start-all");
    expect(post.url.searchParams.get("source")).toBe("7");
    expect(go.oks).toBe(1);
  });
});

describe("stop-all hold-to-confirm", () => {
  const impact = json(200, { destinations: 3, youtubeBroadcasts: 2 });

  it("counts the YouTube broadcasts on press, then sends stop-all for its programme after the hold", async () => {
    const { rt, net, sched } = runtimeFor({
      "GET /api/v1/auth/connection": ADMIN,
      "GET /api/v1/destinations/stop-impact": impact,
      "POST /api/v1/destinations/stop-all": json(200, {}),
    });
    rt.configure(PAIRED);
    await rt.verify();
    const stop = surface("s", "stopAll", { sourceId: 7 });
    rt.add(stop);

    await rt.pressStopDown("s");
    expect(stop.titles.at(-1)).toContain("2 YouTube will end");
    expect(net.writes()).toHaveLength(0);

    sched.fireAll();
    await new Promise((r) => setTimeout(r, 0));
    const post = net.writes()[0]!;
    expect(post.url.pathname).toBe("/api/v1/destinations/stop-all");
    expect(post.url.searchParams.get("source")).toBe("7");
    expect(JSON.parse(post.body!)).toEqual({ confirm: true });
  });

  it("sends no stop when the count cannot be loaded", async () => {
    const { rt, net, sched } = runtimeFor({
      "GET /api/v1/auth/connection": ADMIN,
      "GET /api/v1/destinations/stop-impact": json(500, { error: "store locked" }),
      "POST /api/v1/destinations/stop-all": json(200, {}),
    });
    rt.configure(PAIRED);
    await rt.verify();
    const stop = surface("s", "stopAll", { sourceId: 7 });
    rt.add(stop);

    await rt.pressStopDown("s");
    expect(stop.alerts).toBe(1);
    expect(stop.titles.at(-1)).toContain("Count unavailable");
    expect(sched.count()).toBe(0);
    sched.fireAll();
    expect(net.writes()).toHaveLength(0);
  });

  it("sends no stop when the key is released before the hold completes", async () => {
    const { rt, net, sched } = runtimeFor({
      "GET /api/v1/auth/connection": ADMIN,
      "GET /api/v1/destinations/stop-impact": impact,
      "POST /api/v1/destinations/stop-all": json(200, {}),
    });
    rt.configure(PAIRED);
    await rt.verify();
    rt.add(surface("s", "stopAll", { sourceId: 7 }));

    await rt.pressStopDown("s");
    await rt.pressStopUp("s");
    sched.fireAll();
    expect(net.writes()).toHaveLength(0);
  });
});

describe("destination keys", () => {
  it("paints a running destination as live from its own programme's status", async () => {
    const { rt } = runtimeFor({
      "GET /api/v1/auth/connection": READ,
      "GET /api/v1/status": json(200, STATUS_7),
    });
    rt.configure(PAIRED);
    const key = surface("d", "destination", { sourceId: 7, destinationId: 5 });
    rt.add(key);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.live);
    expect(key.titles.at(-1)).toBe("Live");
  });

  it("paints a destination on another programme as unbound, never with its state", async () => {
    const { rt } = runtimeFor({
      "GET /api/v1/auth/connection": READ,
      "GET /api/v1/status": json(200, { destinations: [{ id: 9, name: "Other", platform: "twitch", enabled: true, sourceId: 9, process: { state: "running" } }] }),
    });
    rt.configure(PAIRED);
    const key = surface("d", "destination", { sourceId: 7, destinationId: 9 });
    rt.add(key);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.unbound);
    expect(key.titles.at(-1)).toBe("Other programme");
  });

  it("starts an off destination with an admin token", async () => {
    const { rt, net } = runtimeFor({
      "GET /api/v1/auth/connection": ADMIN,
      "GET /api/v1/status": json(200, STATUS_7),
      "POST /api/v1/destinations/6/start": json(200, {}),
    });
    rt.configure(PAIRED);
    await rt.verify();
    rt.add(surface("d", "destination", { sourceId: 7, destinationId: 6 }));
    await rt.tick();
    await rt.pressDestination("d");
    expect(net.writes()[0]!.url.pathname).toBe("/api/v1/destinations/6/start");
  });

  it("does not restart a destination that is already on", async () => {
    const { rt, net } = runtimeFor({
      "GET /api/v1/auth/connection": ADMIN,
      "GET /api/v1/status": json(200, STATUS_7),
    });
    rt.configure(PAIRED);
    await rt.verify();
    rt.add(surface("d", "destination", { sourceId: 7, destinationId: 5 }));
    await rt.tick();
    await rt.pressDestination("d");
    expect(net.writes()).toHaveLength(0);
  });
});

describe("pairing states", () => {
  it("paints Not paired when there is no address or token", async () => {
    const { rt } = runtimeFor({});
    rt.configure({});
    const key = surface("d", "destination", { sourceId: 7, destinationId: 5 });
    rt.add(key);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.idle);
    expect(key.titles.at(-1)).toBe("Not paired");
  });

  it("paints Token refused when the install answers 401", async () => {
    const { rt } = runtimeFor({ "GET /api/v1/auth/connection": json(401, { error: "no such token" }) });
    rt.configure(PAIRED);
    const key = surface("d", "destination", { sourceId: 7, destinationId: 5 });
    rt.add(key);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.error);
    expect(key.titles.at(-1)).toBe("Token refused");
  });

  it("paints Unreachable, not a guess about state, when the host does not answer", async () => {
    const { rt } = runtimeFor({ "GET /api/v1/auth/connection": () => new Error("connect ECONNREFUSED") });
    rt.configure(PAIRED);
    const key = surface("d", "destination", { sourceId: 7, destinationId: 5 });
    rt.add(key);
    await rt.tick();
    expect(key.titles.at(-1)).toContain("Unreachable");
  });

  it("refuses an http address for a remote host before any request is made", () => {
    const { rt, net } = runtimeFor({});
    rt.configure({ url: "http://studio.example", token: "t" });
    expect(net.sent).toHaveLength(0);
  });
});

describe("failover key", () => {
  it("hands control back to auto when not on primary", async () => {
    const { rt, net } = runtimeFor({
      "GET /api/v1/auth/connection": ADMIN,
      "GET /api/v1/status": json(200, STATUS_7),
      "POST /api/v1/failover/source": json(200, {}),
    });
    rt.configure(PAIRED);
    await rt.verify();
    rt.add(surface("f", "failover", { sourceId: 7 }));
    await rt.tick();
    await rt.pressFailover("f");
    expect(JSON.parse(net.writes()[0]!.body!)).toEqual({ source: "auto" });
  });

  it("sends nothing when already on primary", async () => {
    const { rt, net } = runtimeFor({
      "GET /api/v1/auth/connection": ADMIN,
      "GET /api/v1/status": json(200, { ...STATUS_7, failover: { active: "primary" } }),
    });
    rt.configure(PAIRED);
    await rt.verify();
    rt.add(surface("f", "failover", { sourceId: 7 }));
    await rt.tick();
    await rt.pressFailover("f");
    expect(net.writes()).toHaveLength(0);
  });
});

describe("dial meter", () => {
  it("shows NO FEED with no frame, and a bar once a frame arrives", async () => {
    const { rt } = runtimeFor({ "GET /api/v1/auth/connection": READ, "GET /api/v1/status": json(200, STATUS_7) });
    rt.configure(PAIRED);
    const dial = surface("m", "meter", {});
    rt.add(dial);
    await rt.tick();
    expect(dial.feedback.at(-1)).toMatchObject({ value: "NO FEED", indicator: 0 });

    rt.ingestFrame({ peak: [[-30]], rms: [[-30]] });
    await rt.tick();
    expect(dial.feedback.at(-1)).toMatchObject({ value: "-30 dB", indicator: 50 });
  });
});
