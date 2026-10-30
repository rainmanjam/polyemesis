import { describe, expect, it } from "vitest";
import type { TransportResponse } from "../src/api.js";
import { COALESCE_MS, FLASH_MS } from "../src/runtime.js";
import { gainForDb, MAX_GAIN } from "../src/trackLevel.js";
import { ADMIN, json, PAIRED, READ, runtimeFor, settle, surface, type Route } from "./fakeInstall.js";

// The Track gain dial: rotate for gain in 0.5 dB steps, coalesced; push to mute;
// touch for unity. The display reads the stored level, never a guess.

function dest(over: Record<string, unknown> = {}, track: Record<string, unknown> = {}) {
  return {
    destination: {
      id: 5,
      name: "Twitch",
      kind: "rtmp",
      sourceId: 7,
      audio: {},
      profile: { mode: "simple", tracks: [{ track: 2, enabled: true, gain: 1, ...track }] },
      ...over,
    },
  };
}

const GAIN_ROUTE = "POST /api/v1/destinations/5/tracks/2/gain";
const applied = (how: "live" | "restart", reason?: string): Route =>
  json(200, { applied: how, ...(reason ? { reason } : {}), destination: dest().destination });

function routes(over: Record<string, Route> = {}): Record<string, Route> {
  return {
    "GET /api/v1/auth/connection": ADMIN,
    "GET /api/v1/destinations": json(200, [dest()]),
    "GET /api/v1/source": json(200, { annotations: [{ track: 2, label: "Guest mic" }] }),
    [GAIN_ROUTE]: applied("live"),
    ...over,
  };
}

async function dialOn(r: Record<string, Route>, settings = { sourceId: 7, destinationId: 5, track: 2 }) {
  const env = runtimeFor(r);
  env.rt.configure(PAIRED);
  const dial = surface("d", "trackGain", settings);
  env.rt.add(dial);
  await env.rt.tick();
  const gains = () => env.net.writes().map((w) => JSON.parse(w.body!) as { gain?: number; muted?: boolean });
  return { ...env, dial, gains };
}

describe("track gain dial: the display", () => {
  it("shows the destination and track, the stored level in dB and a bar", async () => {
    const { dial } = await dialOn(routes());
    expect(dial.feedback.at(-1)).toEqual({ title: "Twitch · Guest mic", value: "0.0 dB", indicator: 87 });
  });

  it("shows a stored gain of 0 as minus infinity, never NaN or Infinity", async () => {
    const { dial } = await dialOn(routes({ "GET /api/v1/destinations": json(200, [dest({}, { gain: 0 })]) }));
    expect(dial.feedback.at(-1)).toMatchObject({ value: "-∞ dB", indicator: 0 });
  });

  it("says MUTED beside the level of a muted track", async () => {
    const { dial } = await dialOn(routes({ "GET /api/v1/destinations": json(200, [dest({}, { muted: true, gain: 0.5 })]) }));
    expect(dial.feedback.at(-1)!.value).toBe("MUTED · -6.0 dB");
  });

  it("says to use the UI on a matrix mix, where the server takes no track gain", async () => {
    const { dial } = await dialOn(routes({ "GET /api/v1/destinations": json(200, [dest({ profile: { mode: "matrix", tracks: [{ track: 2, enabled: true, gain: 1 }] } })]) }));
    expect(dial.feedback.at(-1)).toMatchObject({ value: "matrix: use UI", indicator: 0 });
  });

  it("says NO FEED when the stored levels cannot be read", async () => {
    const { dial } = await dialOn(routes({ "GET /api/v1/destinations": json(500, { error: "boom" }) }));
    expect(dial.feedback.at(-1)).toEqual({ title: "", value: "NO FEED", indicator: 0 });
  });

  it("says what is missing when it is not bound to a track in the selection", async () => {
    const { dial } = await dialOn(routes(), { sourceId: 7, destinationId: 5, track: 4 });
    expect(dial.feedback.at(-1)!.value).toBe("Track not in mix");
  });

  it("shows audio copy as not available", async () => {
    const { dial } = await dialOn(routes({ "GET /api/v1/destinations": json(200, [dest({ audio: { copy: true } })]) }));
    expect(dial.feedback.at(-1)!.value).toBe("Not available");
  });
});

describe("track gain dial: a read token", () => {
  it("sends nothing on a turn, a push or a touch, and says it needs admin", async () => {
    const { rt, net, dial } = await dialOn(routes({ "GET /api/v1/auth/connection": READ }));
    const before = net.sent.length;
    await rt.rotateTrackGain("d", 3);
    await rt.pushTrackGain("d");
    await rt.touchTrackGain("d");
    expect(net.sent.length).toBe(before);
    expect(dial.feedback.at(-1)!.value).toBe("Needs admin token");
    expect(dial.alerts).toBe(3);
  });
});

describe("track gain dial: turning", () => {
  it("coalesces a fast spin into one request for where the spin ended", async () => {
    const { rt, dial, sched, gains } = await dialOn(routes());
    for (let i = 0; i < 10; i++) await rt.rotateTrackGain("d", 1);
    expect(gains()).toHaveLength(0);
    // The dial shows where it is going at once, before the server has it.
    expect(dial.feedback.at(-1)!.value).toBe("+5.0 dB");
    sched.fire(COALESCE_MS);
    await settle();
    expect(gains()).toEqual([{ gain: gainForDb(5) }]);
  });

  // MUTATION: restore the DB_MIN clamp in stepDb. Observed: FAIL.
  it("turns a track stored below the dial's floor up 0.5 dB, not 20 dB to the floor", async () => {
    const { rt, dial, sched, gains } = await dialOn(
      routes({ "GET /api/v1/destinations": json(200, [dest({}, { gain: 0.001 })]) }),
    );
    await rt.rotateTrackGain("d", 1);
    expect(dial.feedback.at(-1)!.value).toBe("-59.5 dB");
    sched.fire(COALESCE_MS);
    await settle();
    expect(gains()).toEqual([{ gain: gainForDb(-59.5) }]);
    expect(gains()[0]!.gain!).toBeLessThan(0.0011);
  });

  it("sends the latest level once after a request in flight returns, not one per tick", async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const slow = (async () => {
      await gate;
      return { status: 200, body: JSON.stringify({ applied: "live", destination: dest().destination }) } as TransportResponse;
    }) as unknown as Route;
    const { rt, sched, gains } = await dialOn(routes({ [GAIN_ROUTE]: slow }));
    await rt.rotateTrackGain("d", -2);
    sched.fire(COALESCE_MS);
    await settle();
    expect(gains()).toEqual([{ gain: gainForDb(-1) }]);
    for (let i = 0; i < 6; i++) await rt.rotateTrackGain("d", -1);
    sched.fire(COALESCE_MS);
    await settle();
    expect(gains()).toHaveLength(1);
    release();
    await settle();
    sched.fire(COALESCE_MS);
    await settle();
    expect(gains()).toEqual([{ gain: gainForDb(-1) }, { gain: gainForDb(-4) }]);
  });

  it("keeps the turned level on the display while it is pending, whatever the status tick reads", async () => {
    const { rt, dial } = await dialOn(routes());
    await rt.rotateTrackGain("d", 1);
    await rt.tick();
    expect(dial.feedback.at(-1)!.value).toBe("+0.5 dB");
  });

  it("clamps to the server's cap, and sends nothing for a turn past an end", async () => {
    const { rt, sched, gains } = await dialOn(routes());
    await rt.rotateTrackGain("d", 100);
    sched.fire(COALESCE_MS);
    await settle();
    expect(gains()[0]!.gain).toBeLessThanOrEqual(MAX_GAIN);
    expect(gains()[0]!.gain).toBe(gainForDb(6));

    const top = await dialOn(routes({ "GET /api/v1/destinations": json(200, [dest({}, { gain: MAX_GAIN })]) }));
    await top.rt.rotateTrackGain("d", 1);
    top.sched.fireAll();
    await settle();
    expect(top.gains()).toHaveLength(0);
  });

  it("sends no gain on a matrix mix or an audio-copy destination", async () => {
    for (const over of [{ profile: { mode: "matrix", tracks: [{ track: 2, enabled: true, gain: 1 }] } }, { audio: { copy: true } }]) {
      const { rt, sched, gains, dial } = await dialOn(routes({ "GET /api/v1/destinations": json(200, [dest(over)]) }));
      await rt.rotateTrackGain("d", 1);
      await rt.touchTrackGain("d");
      sched.fireAll();
      await settle();
      expect(gains()).toHaveLength(0);
      expect(dial.alerts).toBe(2);
    }
  });

  it("shows a 400 as not available with the server's words kept, and drops the pending level", async () => {
    const { rt, sched, dial } = await dialOn(routes({ [GAIN_ROUTE]: json(400, { error: "a matrix mix takes its gain per cell" }) }));
    await rt.rotateTrackGain("d", 1);
    sched.fire(COALESCE_MS);
    await settle();
    expect(dial.feedback.at(-1)!.value).toBe("Not available");
    expect(dial.alerts).toBe(1);
  });

  it("shows a restart, which the viewer saw, then the level", async () => {
    const { rt, sched, dial } = await dialOn(routes({ [GAIN_ROUTE]: applied("restart", "Windows has no live path") }));
    await rt.rotateTrackGain("d", 1);
    sched.fire(COALESCE_MS);
    await settle();
    expect(dial.feedback.at(-1)!.value).toBe("Applied by restart");
    await rt.tick();
    expect(dial.feedback.at(-1)!.value).toBe("Applied by restart");
    sched.fire(FLASH_MS);
    await settle();
    expect(dial.feedback.at(-1)!.value).toBe("0.0 dB");
  });
});

describe("track gain dial: push and touch", () => {
  it("push toggles mute, matrix mix or not, and says how it applied", async () => {
    const { rt, gains, dial } = await dialOn(routes({ "GET /api/v1/destinations": json(200, [dest({ profile: { mode: "matrix", tracks: [{ track: 2, enabled: true, gain: 1 }] } })]) }));
    await rt.pushTrackGain("d");
    expect(gains()).toEqual([{ muted: true }]);
    expect(dial.feedback.at(-1)!.value).toBe("Applied live");
  });

  it("touch resets to unity at once, cancelling a pending turn", async () => {
    const { rt, sched, gains } = await dialOn(routes({ "GET /api/v1/destinations": json(200, [dest({}, { gain: 0.5 })]) }));
    await rt.rotateTrackGain("d", 3);
    await rt.touchTrackGain("d");
    await settle();
    expect(gains()).toEqual([{ gain: 1 }]);
    sched.fireAll();
    await settle();
    expect(gains()).toEqual([{ gain: 1 }]);
  });
});

describe("track gain dial: a double push", () => {
  // MUTATION: call flipDialMute directly from pushTrackGain. Observed: FAIL.
  it("ignores a second push while the first is out", async () => {
    let muted = false;
    const row = () => dest({}, { muted });
    const { rt, gains } = await dialOn(
      routes({
        "GET /api/v1/destinations": () => ({ status: 200, body: JSON.stringify([row()]) }),
        [GAIN_ROUTE]: (req) => {
          muted = (JSON.parse(req.body!) as { muted: boolean }).muted;
          return { status: 200, body: JSON.stringify({ applied: "live", destination: row().destination }) };
        },
      }),
    );
    await Promise.all([rt.pushTrackGain("d"), rt.pushTrackGain("d")]);
    expect(gains()).toEqual([{ muted: true }]);
  });
});

describe("track gain dial: a status tick older than the dial's answer", () => {
  // MUTATION: store every read in readLevels (drop the ticket check). Observed: FAIL.
  it("does not put the old gain back on the dial", async () => {
    let gain = 1;
    let slow = false;
    let gate!: () => void;
    const row = () => dest({}, { gain });
    const { rt, dial, sched } = await dialOn(
      routes({
        "GET /api/v1/destinations": (() => {
          const body = JSON.stringify([row()]);
          if (!slow) return { status: 200, body };
          // Read now, answered later: the tick's snapshot predates the dial's change.
          return new Promise((r) => (gate = () => r({ status: 200, body })));
        }) as unknown as Route,
        [GAIN_ROUTE]: (req) => {
          gain = (JSON.parse(req.body!) as { gain: number }).gain;
          return { status: 200, body: JSON.stringify({ applied: "live", destination: row().destination }) };
        },
      }),
    );
    slow = true;
    const tick = rt.tick();
    await settle();
    await rt.rotateTrackGain("d", 2);
    sched.fire(COALESCE_MS);
    await settle();
    expect(gain).toBe(gainForDb(1));
    expect(dial.feedback.at(-1)!.value).toBe("+1.0 dB");
    gate();
    await tick;
    expect(dial.feedback.at(-1)!.value).toBe("+1.0 dB");
  });
});
