import { describe, expect, it } from "vitest";
import { KEY_STATE } from "../src/render.js";
import { FLASH_MS } from "../src/runtime.js";
import { ADMIN, json, PAIRED, READ, runtimeFor, settle, surface, type Route } from "./fakeInstall.js";

// The Mute track key: the stored mute shown from GET /destinations, a tap that
// toggles it, and an honest flash of HOW the change reached the output.

function dest(over: Record<string, unknown> = {}, tracks?: unknown[]) {
  return {
    destination: {
      id: 5,
      name: "Twitch",
      kind: "rtmp",
      sourceId: 7,
      audio: {},
      profile: {
        mode: "simple",
        tracks: tracks ?? [
          { track: 0, enabled: true, gain: 1 },
          { track: 2, enabled: true, gain: 1, muted: false },
        ],
      },
      ...over,
    },
  };
}

const SOURCE_7 = json(200, { tracks: [{ index: 0, title: "" }, { index: 2, title: "" }], annotations: [{ track: 2, label: "Guest mic" }] });
const GAIN_ROUTE = "POST /api/v1/destinations/5/tracks/2/gain";

function routes(over: Record<string, Route> = {}): Record<string, Route> {
  return {
    "GET /api/v1/auth/connection": ADMIN,
    "GET /api/v1/destinations": json(200, [dest()]),
    "GET /api/v1/source": SOURCE_7,
    ...over,
  };
}

/** The answer the server gives a mute: the stored row after the change, and how it applied. */
function applied(how: "live" | "restart", muted: boolean, reason?: string): Route {
  return json(200, {
    applied: how,
    ...(reason ? { reason } : {}),
    destination: dest({}, [
      { track: 0, enabled: true, gain: 1 },
      { track: 2, enabled: true, gain: 1, muted },
    ]).destination,
  });
}

async function keyOn(r: Record<string, Route>, settings = { sourceId: 7, destinationId: 5, track: 2 }) {
  const env = runtimeFor(r);
  env.rt.configure(PAIRED);
  const key = surface("k", "trackMute", settings);
  env.rt.add(key);
  await env.rt.tick();
  return { ...env, key };
}

describe("track mute key: what it shows", () => {
  it("shows an unmuted track as live, named by the operator's label", async () => {
    const { key } = await keyOn(routes());
    expect(key.states.at(-1)).toBe(KEY_STATE.live);
    expect(key.titles.at(-1)).toBe("Unmuted\nGuest mic");
  });

  it("shows a muted track on its own red crossed-speaker face", async () => {
    const { key } = await keyOn(routes({ "GET /api/v1/destinations": json(200, [dest({}, [{ track: 2, enabled: true, gain: 1, muted: true }])]) }));
    expect(key.states.at(-1)).toBe(KEY_STATE.muted);
    expect(key.titles.at(-1)).toBe("MUTED\nGuest mic");
  });

  it("picks up a mute made in the UI or over the API on the next status tick", async () => {
    let rows: Route = json(200, [dest()]);
    const { rt, key } = await keyOn(routes({ "GET /api/v1/destinations": (req) => rows(req) }));
    expect(key.states.at(-1)).toBe(KEY_STATE.live);
    rows = json(200, [dest({}, [{ track: 2, enabled: true, gain: 1, muted: true }])]);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.muted);
  });

  it("names track 0 by number when the install has no name for it", async () => {
    const { key } = await keyOn(routes({ "GET /api/v1/source": json(200, { tracks: [] }) }), {
      sourceId: 7,
      destinationId: 5,
      track: 0,
    });
    expect(key.titles.at(-1)).toBe("Unmuted\nTrack 1");
  });

  it("still shows the state when the track names cannot be read", async () => {
    const { key } = await keyOn(routes({ "GET /api/v1/source": json(500, { error: "boom" }) }));
    expect(key.titles.at(-1)).toBe("Unmuted\nTrack 3");
  });

  it("is unbound with no track, a track outside the selection, or a destination on another programme", async () => {
    expect((await keyOn(routes(), { sourceId: 7, destinationId: 5 } as never)).key.states.at(-1)).toBe(KEY_STATE.unbound);
    const outside = await keyOn(routes(), { sourceId: 7, destinationId: 5, track: 4 });
    expect(outside.key.states.at(-1)).toBe(KEY_STATE.unbound);
    expect(outside.key.titles.at(-1)).toBe("Track not in mix");
    const other = await keyOn(routes(), { sourceId: 9, destinationId: 5, track: 2 });
    expect(other.key.titles.at(-1)).toBe("Other programme");
    const gone = await keyOn(routes(), { sourceId: 7, destinationId: 6, track: 2 });
    expect(gone.key.titles.at(-1)).toBe("Not found");
  });

  it("shows audio copy as not available, with the reason, before any press", async () => {
    const { key } = await keyOn(routes({ "GET /api/v1/destinations": json(200, [dest({ audio: { copy: true } })]) }));
    expect(key.states.at(-1)).toBe(KEY_STATE.unavailable);
    expect(key.titles.at(-1)).toBe("Not available\nAudio passes through");
  });

  it("says unreachable, never the old state, when the destination list cannot be read", async () => {
    let rows: Route = json(200, [dest()]);
    const { rt, key } = await keyOn(routes({ "GET /api/v1/destinations": (req) => rows(req) }));
    rows = () => new Error("ECONNRESET");
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.error);
  });

  it("does not read /status for its own face", async () => {
    const { net } = await keyOn(routes());
    expect(net.sent.some((r) => r.url.pathname === "/api/v1/status")).toBe(false);
  });
});

describe("track mute key: a press", () => {
  it("on a read token sends nothing at all and says it needs admin", async () => {
    const { rt, net, key } = await keyOn(routes({ "GET /api/v1/auth/connection": READ, [GAIN_ROUTE]: applied("live", true) }));
    const before = net.sent.length;
    await rt.pressTrackMute("k");
    expect(net.sent.length).toBe(before);
    expect(net.writes()).toHaveLength(0);
    expect(key.states.at(-1)).toBe(KEY_STATE.needsAdmin);
    expect(key.titles.at(-1)).toBe("Needs admin token");
    expect(key.alerts).toBe(1);
  });

  it("mutes an unmuted track, on the key's programme, and flashes that it applied live", async () => {
    const { rt, net, key, sched } = await keyOn(routes({ [GAIN_ROUTE]: applied("live", true) }));
    await rt.pressTrackMute("k");
    const post = net.writes()[0]!;
    expect(post.url.pathname).toBe("/api/v1/destinations/5/tracks/2/gain");
    expect(post.url.searchParams.get("source")).toBe("7");
    expect(JSON.parse(post.body!)).toEqual({ muted: true });
    expect(key.states.at(-1)).toBe(KEY_STATE.muted);
    expect(key.titles.at(-1)).toBe("MUTED\nGuest mic\nApplied live");
    expect(key.oks).toBe(1);
    sched.fire(FLASH_MS);
    await settle();
    expect(key.titles.at(-1)).toBe("MUTED\nGuest mic");
  });

  it("unmutes a muted track, reading the switch fresh at the press", async () => {
    let rows: Route = json(200, [dest()]);
    const { rt, net } = await keyOn(routes({ "GET /api/v1/destinations": (req) => rows(req), [GAIN_ROUTE]: applied("live", false) }));
    // Muted in the UI since the last tick: the press must unmute, not mute again.
    rows = json(200, [dest({}, [{ track: 2, enabled: true, gain: 1, muted: true }])]);
    await rt.pressTrackMute("k");
    expect(JSON.parse(net.writes()[0]!.body!)).toEqual({ muted: false });
  });

  it("shows a restart on its own face with the server's reason, and a tick does not paint over it", async () => {
    const reason = "the platform has no verified live path";
    // The install stores the mute, so the next tick's list says muted, as the real one would.
    let muted = false;
    const { rt, key, sched } = await keyOn(routes({
      "GET /api/v1/destinations": () => json(200, [dest({}, [{ track: 2, enabled: true, gain: 1, muted }])])({} as never),
      [GAIN_ROUTE]: (req) => {
        muted = true;
        return applied("restart", true, reason)(req);
      },
    }));
    await rt.pressTrackMute("k");
    expect(key.states.at(-1)).toBe(KEY_STATE.restarted);
    expect(key.titles.at(-1)).toBe(`Applied by restart\n${reason.slice(0, 27)}…`);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.restarted);
    sched.fire(FLASH_MS);
    await settle();
    expect(key.states.at(-1)).toBe(KEY_STATE.muted);
  });

  it.each([
    [409, "this destination passes its audio through unchanged"],
    [400, "a matrix mix takes its gain per cell"],
  ])("maps a %i to not available, with the server's words, and keeps it there", async (status, message) => {
    const { rt, key } = await keyOn(routes({ [GAIN_ROUTE]: json(status, { error: message }) }));
    await rt.pressTrackMute("k");
    expect(key.states.at(-1)).toBe(KEY_STATE.unavailable);
    expect(key.titles.at(-1)!.startsWith("Not available\n")).toBe(true);
    expect(key.alerts).toBe(1);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.unavailable);
  });

  it("maps a 404 to the track not being in the mix", async () => {
    const { rt, key } = await keyOn(routes({ [GAIN_ROUTE]: json(404, { error: "track is not part of this destination's selection" }) }));
    await rt.pressTrackMute("k");
    expect(key.states.at(-1)).toBe(KEY_STATE.unbound);
    expect(key.titles.at(-1)!.startsWith("Track not in mix")).toBe(true);
  });

  it("sends nothing for an audio-copy destination", async () => {
    const { rt, net, key } = await keyOn(routes({ "GET /api/v1/destinations": json(200, [dest({ audio: { copy: true } })]), [GAIN_ROUTE]: applied("live", true) }));
    await rt.pressTrackMute("k");
    expect(net.writes()).toHaveLength(0);
    expect(key.alerts).toBe(1);
  });

  it("sends nothing for a destination on another programme, or a track outside its selection", async () => {
    for (const settings of [
      { sourceId: 9, destinationId: 5, track: 2 },
      { sourceId: 7, destinationId: 5, track: 4 },
    ]) {
      const { rt, net, key } = await keyOn(routes({ [GAIN_ROUTE]: applied("live", true) }), settings);
      await rt.pressTrackMute("k");
      expect(net.writes()).toHaveLength(0);
      expect(key.alerts).toBe(1);
    }
  });

  it("mutes a matrix mix's track: the server applies mute there, it refuses only gain", async () => {
    const { rt, net } = await keyOn(routes({
      "GET /api/v1/destinations": json(200, [dest({ profile: { mode: "matrix", tracks: [{ track: 2, enabled: true, gain: 1 }] } })]),
      [GAIN_ROUTE]: applied("live", true),
    }));
    await rt.pressTrackMute("k");
    expect(JSON.parse(net.writes()[0]!.body!)).toEqual({ muted: true });
  });

  it("alerts and sends nothing when the fresh read fails", async () => {
    let rows: Route = json(200, [dest()]);
    const { rt, net, key } = await keyOn(routes({ "GET /api/v1/destinations": (req) => rows(req), [GAIN_ROUTE]: applied("live", true) }));
    rows = json(500, { error: "boom" });
    await rt.pressTrackMute("k");
    expect(net.writes()).toHaveLength(0);
    expect(key.alerts).toBe(1);
  });

  it("alerts on a failed write without claiming anything applied", async () => {
    const { rt, key } = await keyOn(routes({ [GAIN_ROUTE]: () => new Error("ECONNRESET") }));
    await rt.pressTrackMute("k");
    expect(key.alerts).toBe(1);
    expect(key.oks).toBe(0);
    expect(key.states.at(-1)).toBe(KEY_STATE.live);
  });
});

describe("destinationChoices (the inspector's pickers)", () => {
  it("lists the programme's destinations with their selected tracks, named, and no other programme's", async () => {
    const { install } = await import("./fakeInstall.js");
    const { PolyemesisClient } = await import("../src/api.js");
    const { destinationChoices } = await import("../src/runtime.js");
    const net = install({
      "GET /api/v1/destinations": json(200, [dest(), dest({ id: 9, name: "Other", sourceId: 8 })]),
      "GET /api/v1/source": SOURCE_7,
    });
    const client = new PolyemesisClient(new URL(PAIRED.url), "t", net.transport);
    expect(await destinationChoices(client, 7, "en")).toEqual([
      { id: 5, name: "Twitch", tracks: [{ track: 0, name: "Track 1" }, { track: 2, name: "Guest mic" }] },
    ]);
    expect(net.sent.find((r) => r.url.pathname === "/api/v1/source")!.url.searchParams.get("source")).toBe("7");
    expect((await destinationChoices(client, undefined, "en")).map((d) => d.id)).toEqual([5, 9]);
  });

  it("still lists the destinations when the names cannot be read", async () => {
    const { install } = await import("./fakeInstall.js");
    const { PolyemesisClient } = await import("../src/api.js");
    const { destinationChoices } = await import("../src/runtime.js");
    const net = install({ "GET /api/v1/destinations": json(200, [dest()]) });
    const client = new PolyemesisClient(new URL(PAIRED.url), "t", net.transport);
    expect((await destinationChoices(client, 7, "de"))[0]!.tracks[1]!.name).toBe("Spur 3");
  });
});
