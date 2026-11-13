import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { DESTS, GAIN_MAX, GAIN_MIN, GAIN_STEP, HOLD_MS, KEYS, LOOP, REST, REST_LOG, START, createStore, dialDesc, logFor, keyDesc, step, takeOver, type Action, type DeckState } from "./state";

const off = (): DeckState => ({ dests: ["off", "off", "off", "off", "off"], gain: [0, 0], muted: [false, false], held: null, viewers: 0 });

describe("step", () => {
  it("golive moves every off destination to starting and settle moves them to live", () => {
    const s = step(step(off(), { type: "golive" }), { type: "settle" });
    expect(step(off(), { type: "golive" }).dests).toEqual(Array(5).fill("starting"));
    expect(s.dests).toEqual(Array(5).fill("live"));
  });

  it("golive leaves a live destination alone", () => {
    const s = { ...off(), dests: ["live", "off", "off", "off", "off"] as DeckState["dests"] };
    expect(step(s, { type: "golive" }).dests).toEqual(["live", "starting", "starting", "starting", "starting"]);
  });

  it("a tap on a failed destination retries it: fail, then dest, gives starting", () => {
    const failed = step(step(step(off(), { type: "golive" }), { type: "fail", index: 1 }), { type: "settle" });
    expect(failed.dests[1]).toBe("failed");
    expect(step(failed, { type: "dest", index: 1 }).dests[1]).toBe("starting");
  });

  it("a tap on an off destination starts it", () => {
    expect(step(off(), { type: "dest", index: 2 }).dests[2]).toBe("starting");
  });

  it("a tap on a live destination changes nothing", () => {
    const s = { ...off(), dests: ["live", "off", "off", "off", "off"] as DeckState["dests"] };
    expect(step(s, { type: "dest", index: 0 })).toEqual(s);
  });

  it("fail only fails a destination that is starting", () => {
    expect(step(off(), { type: "fail", index: 0 }).dests[0]).toBe("off");
  });

  it("settle puts the audience on air while anything is live and takes it away when nothing is", () => {
    const live = step(step(off(), { type: "golive" }), { type: "settle" });
    expect(live.viewers).toBeGreaterThan(0);
    expect(step(live, { type: "stopall" }).viewers).toBe(0);
  });

  it("gain moves by 0.5 dB per step, either way, and stays inside -40..+6", () => {
    expect(step(off(), { type: "gain", dial: 0, steps: 3 }).gain).toEqual([1.5, 0]);
    expect(step(off(), { type: "gain", dial: 1, steps: -2 }).gain).toEqual([0, -1]);
    expect(step(off(), { type: "gain", dial: 0, steps: 1000 }).gain[0]).toBe(6);
    expect(step(off(), { type: "gain", dial: 0, steps: -1000 }).gain[0]).toBe(-40);
  });

  it("gain does not accumulate floating point error", () => {
    let s = off();
    for (let i = 0; i < 7; i++) s = step(s, { type: "gain", dial: 0, steps: 1 });
    expect(s.gain[0]).toBe(3.5);
  });

  it("mute toggles one dial and zero resets that dial's gain to 0", () => {
    const m = step(off(), { type: "mute", dial: 1 });
    expect(m.muted).toEqual([false, true]);
    expect(step(m, { type: "mute", dial: 1 }).muted).toEqual([false, false]);
    const g = step(off(), { type: "gain", dial: 1, steps: -6 });
    expect(step(g, { type: "zero", dial: 1 }).gain).toEqual([0, 0]);
  });

  it("stopall turns every destination off and clears the hold", () => {
    const s = step({ ...REST, held: "stopall" }, { type: "stopall" });
    expect(s.dests).toEqual(Array(5).fill("off"));
    expect(s.held).toBeNull();
  });

  it("hold shows the confirm face, release clears it, and preset applies without changing destinations", () => {
    const h = step(off(), { type: "hold", key: "preset" });
    expect(h.held).toBe("preset");
    expect(step(h, { type: "release" }).held).toBeNull();
    expect(step(h, { type: "preset" })).toEqual(off());
  });

  it("never mutates the state it is given", () => {
    const s = off();
    const frozen = Object.freeze({ ...s, dests: Object.freeze([...s.dests]) as DeckState["dests"], gain: Object.freeze([...s.gain]) as DeckState["gain"] });
    expect(() => step(frozen, { type: "golive" })).not.toThrow();
  });
});

describe("DESTS", () => {
  it("names the five destinations the key row shows", () => {
    expect(DESTS).toEqual([
      { platform: "youtube", name: "Club TV" },
      { platform: "twitch", name: "Twitch" },
      { platform: "kick", name: "Kick" },
      { platform: "facebook", name: "Club page" },
      { platform: "x", name: "X" },
    ]);
  });
});

describe("LOOP", () => {
  it("is ordered by time and ends within 26 s", () => {
    const times = LOOP.map((e) => e.at);
    expect(times).toEqual([...times].sort((a, b) => a - b));
    expect(times.at(-1)!).toBeLessThanOrEqual(26);
    expect(times.at(-1)!).toBeGreaterThan(20);
  });

  it("fails a destination and later taps the same one", () => {
    const failAt = LOOP.findIndex((e) => e.action.type === "fail");
    expect(failAt).toBeGreaterThanOrEqual(0);
    const index = (LOOP[failAt].action as { index: number }).index;
    expect(LOOP.slice(failAt + 1).some((e) => e.action.type === "dest" && e.action.index === index)).toBe(true);
  });

  it("writes the lines the plugin would: literal expectations for a fail, a retry and a gain turn", () => {
    const log = (pred: (e: (typeof LOOP)[number]) => boolean) => LOOP.find(pred)!.log;
    expect(log((e) => e.action.type === "golive")).toBe("tap Go live →  5 destinations starting");
    expect(log((e) => e.action.type === "fail")).toBe("fail Twitch →  Failed: Timed out");
    expect(log((e) => e.action.type === "dest" && e.action.index === 1)).toBe("tap Twitch →  retrying");
    expect(log((e) => e.action.type === "gain" && e.action.dial === 1)).toBe("turn Twitch · Mic →  -3.0 dB, applied live");
    expect(log((e) => e.action.type === "stopall")).toBe("hold Stop all →  5 stopped (1 YouTube broadcast ended)");
    expect(LOOP.filter((e) => e.action.type === "settle").every((e) => e.log === "")).toBe(true);
  });

  it("has every destination live once the failed one is retried, and ends with only X back on air", () => {
    let s = off();
    const before = LOOP.findIndex((e) => e.action.type === "stopall");
    LOOP.slice(0, before).forEach((e) => (s = step(s, e.action)));
    expect(s.dests).toEqual(Array(5).fill("live"));
    LOOP.slice(before).forEach((e) => (s = step(s, e.action)));
    expect(s.dests).toEqual(["off", "off", "off", "off", "live"]);
    expect(s.held).toBeNull();
  });
});

describe("REST", () => {
  it("is the state the still shows: everything live, +1.5 and -3.0 dB, 1,284 viewers", () => {
    expect(REST.dests).toEqual(Array(5).fill("live"));
    expect(REST.gain).toEqual([1.5, -3]);
    expect(REST.viewers).toBe(1284);
    expect(REST.held).toBeNull();
  });

  it("has four log lines the loop would have written, newest first", () => {
    expect(REST_LOG).toHaveLength(4);
    expect(REST_LOG[0].what).toMatch(/Twitch · Mic/);
  });
});

describe("createStore", () => {
  it("dispatch steps the state and tells subscribers once; unsubscribe stops it", () => {
    const store = createStore(off());
    let calls = 0;
    const un = store.subscribe(() => calls++);
    store.dispatch({ type: "golive" });
    expect(store.get().dests[0]).toBe("starting");
    expect(calls).toBe(1);
    un();
    store.dispatch({ type: "settle" });
    expect(calls).toBe(1);
  });

  it("does not notify when an action changes nothing", () => {
    const store = createStore(REST);
    let calls = 0;
    store.subscribe(() => calls++);
    store.dispatch({ type: "dest", index: 0 } as Action);
    expect(calls).toBe(0);
  });
});

describe("the legend's words", () => {
  it("has one row per key, in key order, each a tap or a hold as the docs say", () => {
    expect(KEYS.map((k) => [k.name, k.kind])).toEqual([
      ["Go live", "tap"],
      ["Stop all", "hold"],
      ["Routing preset", "hold"],
      ["Club TV", "tap"],
      ["Twitch", "tap"],
      ["Kick", "tap"],
      ["Club page", "tap"],
      ["X", "tap"],
    ]);
  });

  it("describes each destination by its state, and says what a tap does", () => {
    expect(keyDesc(START, 3)).toBe("Off · tap starts it");
    expect(keyDesc(step(START, { type: "golive" }), 3)).toBe("Starting");
    expect(keyDesc(REST, 3)).toBe("Live");
    const failed = step(step(START, { type: "golive" }), { type: "fail", index: 1 });
    expect(keyDesc(failed, 4)).toBe("Failed: Timed out · tap retries");
  });

  it("describes a dial by its level, with MUTED first when it is muted", () => {
    expect(dialDesc(REST, 0)).toBe("+1.5 dB");
    expect(dialDesc(REST, 1)).toBe("-3.0 dB");
    expect(dialDesc(step(REST, { type: "mute", dial: 1 }), 1)).toBe("MUTED · -3.0 dB");
  });
});

describe("takeOver", () => {
  it("puts a hold the show had started back up and settles what was starting", () => {
    const mid: DeckState = { ...REST, dests: ["live", "starting", "live", "off", "live"], held: "preset" };
    const next = takeOver(mid);
    expect(next.held).toBeNull(); // the old behaviour left this "preset", with no action left to complete it
    expect(next.dests).toEqual(["live", "live", "live", "off", "live"]);
  });

  it("leaves a state with nothing in flight exactly as it was", () => {
    expect(takeOver(REST)).toBe(REST);
    expect(takeOver(START)).toBe(START);
  });

  it("after a takeover mid-hold, a visitor's Stop all hold can start again", () => {
    const next = step(takeOver({ ...REST, held: "stopall" }), { type: "hold", key: "stopall" });
    expect(next.held).toBe("stopall");
  });
});

describe("the hold length", () => {
  it("is the plugin's HOLD_MS", () => {
    const src = readFileSync(new URL("../../../../../integrations/streamdeck/src/runtime.ts", import.meta.url), "utf8");
    expect(Number(/export const HOLD_MS = (\d+);/.exec(src)?.[1])).toBe(HOLD_MS);
  });
});

describe("the gain dial's range", () => {
  it("is the plugin's DB_STEP, DB_MIN and DB_MAX", () => {
    const src = readFileSync(new URL("../../../../../integrations/streamdeck/src/trackLevel.ts", import.meta.url), "utf8");
    const num = (name: string) => Number(new RegExp(`export const ${name} = (-?[\\d.]+);`).exec(src)?.[1]);
    expect(num("DB_STEP")).toBe(GAIN_STEP);
    expect(num("DB_MIN")).toBe(GAIN_MIN);
    expect(num("DB_MAX")).toBe(GAIN_MAX);
  });
});

describe("logFor release", () => {
  it("a key let go before its hold completes is logged as released early, and a release with nothing held is not logged", () => {
    const held = step(REST, { type: "hold", key: "preset" });
    const entry = logFor(held, { type: "release" }, step(held, { type: "release" }));
    expect(entry).toMatchObject({ verb: "hold", what: "Routing preset", audio: true });
    expect(entry?.res).toMatch(/released early, nothing changed/);
    const stop = step(REST, { type: "hold", key: "stopall" });
    expect(logFor(stop, { type: "release" }, step(stop, { type: "release" }))).toMatchObject({ what: "Stop all", audio: false });
    expect(logFor(REST, { type: "release" }, REST)).toBeNull();
  });
});
