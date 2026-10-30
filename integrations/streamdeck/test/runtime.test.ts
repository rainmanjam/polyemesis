import { describe, expect, it } from "vitest";
import type { Transport, TransportRequest, TransportResponse } from "../src/api.js";
import { ApiError } from "../src/api.js";
import { Runtime, authKeyFor, type Surface, type SurfaceKind } from "../src/runtime.js";
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
    expect(key.titles.at(-1)).toMatch(/^Token refused\n/);
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
  it("shows NO FEED with no answer from the server, and a bar once the server reports a live reading", async () => {
    let levels: Route = () => ({ status: 500, body: '{"error":"no levels"}' });
    const { rt, net } = runtimeFor({
      "GET /api/v1/auth/connection": READ,
      "GET /api/v1/status": json(200, STATUS_7),
      "GET /api/v1/levels": (req) => levels(req),
    });
    rt.configure(PAIRED);
    const dial = surface("m", "meter", { sourceId: 7 });
    rt.add(dial);
    await rt.tick();
    await rt.tickMeters();
    expect(dial.feedback.at(-1)).toMatchObject({ value: "NO FEED", indicator: 0 });

    levels = json(200, { levels: { peak: [[-30]], rms: [[-30]] }, feed: "live" });
    await rt.tickMeters();
    expect(dial.feedback.at(-1)).toMatchObject({ value: "-30 dB", indicator: 50 });
    expect(net.sent.find((r) => r.url.pathname === "/api/v1/levels")!.url.searchParams.get("source")).toBe("7");
  });

  it("shows NO FEED when the server says the feed is stale, even if the last reading is still on the wire", async () => {
    const { rt } = runtimeFor({
      "GET /api/v1/auth/connection": READ,
      "GET /api/v1/status": json(200, STATUS_7),
      "GET /api/v1/levels": json(200, { levels: { peak: [[-10]], rms: [[-10]] }, feed: "stale" }),
    });
    rt.configure(PAIRED);
    const dial = surface("m", "meter", { sourceId: 7 });
    rt.add(dial);
    await rt.tick();
    await rt.tickMeters();
    expect(dial.feedback.at(-1)).toMatchObject({ value: "NO FEED", indicator: 0 });
  });

  it("sends a repaint only when the face changes", async () => {
    const { rt } = runtimeFor({
      "GET /api/v1/auth/connection": READ,
      "GET /api/v1/status": json(200, STATUS_7),
      "GET /api/v1/levels": json(200, { levels: { peak: [[-30]], rms: [[-30]] }, feed: "live" }),
    });
    rt.configure(PAIRED);
    const dial = surface("m", "meter", { sourceId: 7 });
    rt.add(dial);
    await rt.tick();
    await rt.tickMeters();
    await rt.tickMeters();
    await rt.tickMeters();
    expect(dial.feedback).toHaveLength(1);
  });
});

describe("status that goes stale", () => {
  it("drops a destination from Live to unreachable when a later status read fails", async () => {
    let statusOk = true;
    const { rt } = runtimeFor({
      "GET /api/v1/auth/connection": READ,
      "GET /api/v1/status": (req) => (statusOk ? json(200, STATUS_7)(req) : new Error("connect ECONNREFUSED")),
    });
    rt.configure(PAIRED);
    const key = surface("d", "destination", { sourceId: 7, destinationId: 5 });
    rt.add(key);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.live);

    statusOk = false;
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.error);
    expect(key.titles.at(-1)).toMatch(/^Unreachable/);
    expect(key.titles.at(-1)).not.toMatch(/^Live/);
  });
});

describe("connection that starts unreachable", () => {
  it("retries the check on a later tick and shows live state once the install answers", async () => {
    let reachable = false;
    const { rt } = runtimeFor({
      "GET /api/v1/auth/connection": (req) => (reachable ? READ(req) : new Error("connect ECONNREFUSED")),
      "GET /api/v1/status": json(200, STATUS_7),
    });
    rt.configure(PAIRED);
    const key = surface("d", "destination", { sourceId: 7, destinationId: 5 });
    rt.add(key);
    await rt.tick();
    expect(key.titles.at(-1)).toContain("Unreachable");

    reachable = true;
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.live);
    expect(key.titles.at(-1)).toBe("Live");
  });

  it("does not retry a refused token, so a bad credential is not re-sent every tick", async () => {
    let attempts = 0;
    const { rt } = runtimeFor({
      "GET /api/v1/auth/connection": () => {
        attempts++;
        return { status: 401, body: '{"error":"no such token"}' };
      },
    });
    rt.configure(PAIRED);
    rt.add(surface("d", "destination", { sourceId: 7, destinationId: 5 }));
    await rt.tick();
    await rt.tick();
    await rt.tick();
    expect(attempts).toBe(1);
  });
});

describe("dial after the install stops answering", () => {
  it("goes to NO FEED when the token is revoked, instead of holding the last bar", async () => {
    // A revoked token refuses every route. The dial-only deck below polls only /levels,
    // so the refusal has to be seen there, not only on /status.
    let revoked = false;
    const refused = (ok: Route): Route => (req) => (revoked ? { status: 401, body: '{"error":"revoked"}' } : ok(req) as TransportResponse);
    const { rt } = runtimeFor({
      "GET /api/v1/auth/connection": ADMIN,
      "GET /api/v1/status": refused(json(200, STATUS_7)),
      "GET /api/v1/levels": refused(json(200, { levels: { peak: [[-30]], rms: [[-30]] }, feed: "live" })),
    });
    rt.configure(PAIRED);
    await rt.verify();
    const dial = surface("m", "meter", { sourceId: 7 });
    rt.add(dial);
    await rt.tick();
    await rt.tickMeters();
    expect(dial.feedback.at(-1)).toMatchObject({ value: "-30 dB" });
    expect(rt.isAdmin).toBe(true);

    revoked = true;
    await rt.tickMeters();
    expect(dial.feedback.at(-1)).toMatchObject({ value: "NO FEED", indicator: 0 });
    // A dial-only deck never polls /status, so the refusal must also drop the admin state here.
    expect(rt.isAdmin).toBe(false);
  });

  it("paints an unpaired install's dial as NO FEED rather than leaving it unpainted", async () => {
    const { rt } = runtimeFor({});
    rt.configure({});
    const dial = surface("m", "meter", {});
    rt.add(dial);
    await rt.tick();
    await rt.tickMeters();
    expect(dial.feedback.at(-1)).toMatchObject({ value: "NO FEED" });
  });
});

describe("overlapping clock ticks", () => {
  it("does not stack meter or status requests while one is still in flight", async () => {
    const counts = { levels: 0, status: 0 };
    const transport: Transport = {
      async send(req) {
        const path = req.url.pathname;
        if (path.endsWith("/auth/connection")) return { status: 200, body: JSON.stringify({ name: "x", prefix: "p", scope: "read", adminScope: false }) };
        if (path.endsWith("/levels")) {
          counts.levels++;
          return new Promise<TransportResponse>(() => {});
        }
        if (path.endsWith("/status")) {
          counts.status++;
          return new Promise<TransportResponse>(() => {});
        }
        return { status: 404, body: "{}" };
      },
    };
    const sched = manualScheduler();
    const rt = new Runtime("en", () => transport, sched.schedule);
    rt.configure(PAIRED);
    await rt.verify();
    rt.add(surface("m", "meter", { sourceId: 7 }));
    rt.add(surface("d", "destination", { sourceId: 7, destinationId: 5 }));

    void rt.tickMeters();
    void rt.tickMeters();
    void rt.tickMeters();
    void rt.tick();
    void rt.tick();
    await new Promise((r) => setTimeout(r, 0));
    expect(counts.levels).toBe(1);
    expect(counts.status).toBe(1);
  });
});

describe("armed stop face", () => {
  it("keeps the count and the held state through a periodic repaint", async () => {
    const { rt, sched } = runtimeFor({
      "GET /api/v1/auth/connection": ADMIN,
      "GET /api/v1/status": json(200, STATUS_7),
      "GET /api/v1/destinations/stop-impact": json(200, { destinations: 3, youtubeBroadcasts: 2 }),
    });
    rt.configure(PAIRED);
    await rt.verify();
    const stop = surface("s", "stopAll", { sourceId: 7 });
    rt.add(stop);
    await rt.pressStopDown("s");
    expect(stop.titles.at(-1)).toContain("2 YouTube will end");

    await rt.tick();
    expect(stop.titles.at(-1)).toContain("2 YouTube will end");
    expect(stop.states.at(-1)).toBe(KEY_STATE.held);
    expect(sched.count()).toBe(1);
  });
});

describe("authKeyFor", () => {
  it("names a refused token as refused, and everything else as unreachable", () => {
    expect(authKeyFor(new ApiError("unauthorized", 401, "x"))).toBe("streamdeck.auth.refused");
    expect(authKeyFor(new ApiError("unreachable", undefined, "x"))).toBe("streamdeck.auth.unreachable");
    expect(authKeyFor(new ApiError("untrusted", undefined, "x"))).toBe("streamdeck.auth.unreachable");
  });
});
