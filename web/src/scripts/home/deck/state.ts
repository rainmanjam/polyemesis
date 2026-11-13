/* What the Stream Deck + section knows: the five destinations the key row
 * shows, the two track-gain dials, the audience, and a hold in progress.
 *
 * Everything here is pure data and one reducer, so the legend, the log, the
 * scripted loop and the 3D scene all read and write one state and can never
 * disagree. Nothing in this file touches the DOM or three.js.
 *
 * The destination names are the operator's own and are illustrative (the
 * plugin draws whatever the operator called each destination); the platform
 * ids are the ones the plugin has marks for. */

export type DestState = "off" | "starting" | "live" | "failed";

export interface DeckState {
  dests: DestState[];
  /** Track gain in dB for the first two gain dials (dial 0 is the first destination's track, dial 1 the second's). */
  gain: [number, number];
  muted: [boolean, boolean];
  /** A hold-to-confirm key that is being held down. */
  held: "stopall" | "preset" | null;
  viewers: number;
}

export type Action =
  | { type: "golive" }
  | { type: "stopall" }
  | { type: "preset" }
  | { type: "dest"; index: number }
  | { type: "fail"; index: number }
  | { type: "settle" }
  | { type: "gain"; dial: 0 | 1; steps: number }
  | { type: "mute"; dial: 0 | 1 }
  | { type: "zero"; dial: 0 | 1 }
  /* The two ends of a hold on Stop all or Routing preset: the key shows its
   * confirm face while it is held, and a release before the hold completes
   * changes nothing. A completed hold is the `stopall` or `preset` action. */
  | { type: "hold"; key: "stopall" | "preset" }
  | { type: "release" };

export const DESTS: { platform: string; name: string }[] = [
  { platform: "youtube", name: "Club TV" },
  { platform: "twitch", name: "Twitch" },
  { platform: "kick", name: "Kick" },
  { platform: "facebook", name: "Club page" },
  { platform: "x", name: "X" },
];

/** The plugin's gain step and range (the Track gain dial turns 0.5 dB a step): DB_STEP, DB_MIN and
 *  DB_MAX in integrations/streamdeck/src/trackLevel.ts, which state.test.ts pins these to. */
export const GAIN_STEP = 0.5;
export const GAIN_MIN = -40;
export const GAIN_MAX = 6;
/** How long a hold-to-confirm key must be held: the plugin's HOLD_MS (src/runtime.ts), 1.5 s. */
export const HOLD_MS = 1500;
/** The hold length as the legend writes it ("1.5"), from HOLD_MS so the two cannot drift. */
export const HOLD_SECONDS = String(HOLD_MS / 1000);
const VIEWERS_LIVE = 1284;

/** The title of a gain dial's touch-strip segment: the destination's name and the track. */
export const gainName = (dial: 0 | 1): string => `${DESTS[dial].name} · Mic`;

/** What the state becomes when the visitor takes over from the scripted show: anything the show had
 *  mid-flight is finished or put back, so no key is left on a confirm face the show will never complete. */
export function takeOver(s: DeckState): DeckState {
  let next = s;
  if (next.dests.includes("starting")) next = step(next, { type: "settle" });
  if (next.held) next = step(next, { type: "release" });
  return next;
}

/** A show that has not started: every destination off, every dial at 0 dB. */
export const START: DeckState = { dests: ["off", "off", "off", "off", "off"], gain: [0, 0], muted: [false, false], held: null, viewers: 0 };

/** The resting state, which is also what the still shows. */
export const REST: DeckState = { dests: ["live", "live", "live", "live", "live"], gain: [1.5, -3], muted: [false, false], held: null, viewers: VIEWERS_LIVE };

const round1 = (n: number) => Math.round(n * 10) / 10;
const anyLive = (d: DestState[]) => d.some((x) => x === "live");

export function step(s: DeckState, a: Action): DeckState {
  switch (a.type) {
    case "golive":
      return { ...s, dests: s.dests.map((d) => (d === "off" ? "starting" : d)) };
    case "settle": {
      const dests = s.dests.map((d) => (d === "starting" ? "live" : d));
      return { ...s, dests, viewers: anyLive(dests) ? VIEWERS_LIVE : 0 };
    }
    case "fail":
      return s.dests[a.index] === "starting" ? { ...s, dests: s.dests.map((d, i) => (i === a.index ? "failed" : d)) } : s;
    case "dest": {
      /* A tap starts an off destination and retries a failed one; a live or
       * starting one ignores it. */
      const cur = s.dests[a.index];
      if (cur !== "off" && cur !== "failed") return s;
      return { ...s, dests: s.dests.map((d, i) => (i === a.index ? "starting" : d)) };
    }
    case "stopall":
      return { ...s, dests: s.dests.map(() => "off"), held: null, viewers: 0 };
    case "preset":
      return { ...s, held: null };
    case "hold":
      return { ...s, held: a.key };
    case "release":
      return s.held === null ? s : { ...s, held: null };
    case "gain": {
      const g = [...s.gain] as [number, number];
      g[a.dial] = round1(Math.min(GAIN_MAX, Math.max(GAIN_MIN, g[a.dial] + a.steps * GAIN_STEP)));
      return { ...s, gain: g };
    }
    case "mute": {
      const m = [...s.muted] as [boolean, boolean];
      m[a.dial] = !m[a.dial];
      return { ...s, muted: m };
    }
    case "zero": {
      const g = [...s.gain] as [number, number];
      g[a.dial] = 0;
      return { ...s, gain: g };
    }
  }
}

/* ------------------------------------------------------------------ the log */

export interface LogEntry {
  verb: "tap" | "hold" | "fail" | "turn" | "push" | "touch";
  what: string;
  res: string;
  /** Coral marks an audio control (a dial, the routing preset); nothing else is coral. */
  audio: boolean;
  /** The platform whose mark leads the line, when the line is about one destination. */
  platform?: string;
}
export interface LogRow extends LogEntry {
  /** Seconds on the show clock. */
  t: number;
}

export const fmtDb = (db: number): string => `${db > 0 ? "+" : ""}${(Math.round(db * 10) / 10).toFixed(1)} dB`;
const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? "" : "s"}`;
const liveOrStarting = (s: DeckState) => s.dests.filter((d) => d === "live" || d === "starting").length;
const youtubeLive = (s: DeckState) => (s.dests[0] === "live" ? 1 : 0);

/** What the plugin would tell the operator for an action: the log line, or null when nothing is worth a line. */
export function logFor(prev: DeckState, a: Action, next: DeckState): LogEntry | null {
  switch (a.type) {
    case "golive": {
      const n = prev.dests.filter((d) => d === "off").length;
      const res = n === 0 ? "→  every destination is already live" : `→  ${plural(n, "destination")} starting`;
      return { verb: "tap", what: "Go live", res, audio: false };
    }
    case "fail":
      return prev.dests[a.index] === "starting"
        ? { verb: "fail", what: DESTS[a.index].name, res: "→  Failed: Timed out", audio: false, platform: DESTS[a.index].platform }
        : null;
    case "dest": {
      const cur = prev.dests[a.index];
      const d = DESTS[a.index];
      const res = cur === "off" ? "→  starting" : cur === "failed" ? "→  retrying" : `→  already ${cur === "starting" ? "starting" : "live"}, tap ignored`;
      return { verb: "tap", what: d.name, res, audio: false, platform: d.platform };
    }
    case "stopall": {
      const yt = youtubeLive(prev);
      return { verb: "hold", what: "Stop all", res: `→  ${liveOrStarting(prev)} stopped${yt ? ` (${yt} YouTube broadcast ended)` : ""}`, audio: false };
    }
    case "preset":
      return { verb: "hold", what: "Routing preset", res: `→  No music, applied to ${prev.dests.length} destinations`, audio: true };
    case "release":
      return prev.held === null
        ? null
        : { verb: "hold", what: prev.held === "stopall" ? "Stop all" : "Routing preset", res: "→  released early, nothing changed", audio: prev.held === "preset" };
    case "gain": {
      const d = DESTS[a.dial];
      return { verb: "turn", what: gainName(a.dial), res: `→  ${fmtDb(next.gain[a.dial])}, applied live`, audio: true, platform: d.platform };
    }
    case "mute":
      return { verb: "push", what: gainName(a.dial), res: next.muted[a.dial] ? "→  MUTED" : "→  unmuted", audio: true, platform: DESTS[a.dial].platform };
    case "zero":
      return { verb: "touch", what: gainName(a.dial), res: "→  0.0 dB", audio: true, platform: DESTS[a.dial].platform };
    default:
      return null;
  }
}

export const formatLog = (e: LogEntry | null): string => (e ? `${e.verb} ${e.what} ${e.res}` : "");

/** The four lines the resting state shows, newest first: how the show got there. */
export const REST_LOG: LogRow[] = (() => {
  const go = step(START, { type: "golive" });
  const failed = step(go, { type: "fail", index: 1 });
  const retried = step(failed, { type: "dest", index: 1 });
  const turned = step(REST, { type: "gain", dial: 1, steps: 0 });
  const lines: Array<[number, LogEntry | null]> = [
    [11, logFor(turned, { type: "gain", dial: 1, steps: -6 }, REST)],
    [8, logFor(failed, { type: "dest", index: 1 }, retried)],
    [5, logFor(go, { type: "fail", index: 1 }, failed)],
    [2, logFor(START, { type: "golive" }, go)],
  ];
  return lines.flatMap(([t, e]) => (e ? [{ ...e, t }] : []));
})();

/* --------------------------------------------------------------- the legend */

/** One row per key, in key order. `tip` is the row's tooltip, from docs/STREAM-DECK.md. */
export const KEYS: { name: string; kind: "tap" | "hold"; platform?: string; desc?: string; tip: string }[] = [
  { name: "Go live", kind: "tap", desc: "Starts every destination", tip: "Tap: starts every destination on the key's programme. Needs an admin token." },
  { name: "Stop all", kind: "hold", desc: "Stops all, shows YouTube ends", tip: `Hold ${HOLD_SECONDS} s: stops every destination on the key's programme. Shows how many YouTube broadcasts the stop would end.` },
  { name: "Routing preset", kind: "hold", desc: "Applies a preset to all", tip: "Hold: applies a routing preset to every destination on the key's programme." },
  ...DESTS.map((d) => ({
    name: d.name,
    kind: "tap" as const,
    platform: d.platform,
    tip: "Destination key. Tap: starts it when it is off, retries it when it shows Failed. A live destination ignores the tap.",
  })),
];

const DEST_DESC: Record<DestState, string> = {
  off: "Off · tap starts it",
  starting: "Starting",
  live: "Live",
  failed: "Failed: Timed out · tap retries",
};

/** The legend's second column for a key: what it does, or for a destination its state. */
export const keyDesc = (s: DeckState, key: number): string => (key < 3 ? (KEYS[key].desc ?? "") : DEST_DESC[s.dests[key - 3]]);
/** A gain dial's level as the touch strip shows it. */
export const dialDesc = (s: DeckState, dial: 0 | 1): string => `${s.muted[dial] ? "MUTED · " : ""}${fmtDb(s.gain[dial])}`;

/* ---------------------------------------------------------------- the loop */

/** The scripted show, about 25 s: go live, a destination fails and is retried, two dials, a mute, a preset, a stop. */
export const LOOP: { at: number; action: Action; log: string }[] = (() => {
  const script: Array<[number, Action]> = [
    [1.2, { type: "golive" }],
    [2.7, { type: "fail", index: 1 }],
    [3.6, { type: "settle" }],
    [5.6, { type: "dest", index: 1 }],
    [7.0, { type: "settle" }],
    [8.4, { type: "gain", dial: 1, steps: -6 }],
    [10.8, { type: "gain", dial: 0, steps: 3 }],
    [13.2, { type: "mute", dial: 1 }],
    [15.0, { type: "mute", dial: 1 }],
    [16.4, { type: "hold", key: "preset" }],
    [17.9, { type: "preset" }],
    [20.0, { type: "hold", key: "stopall" }],
    [21.5, { type: "stopall" }],
    [23.4, { type: "dest", index: 4 }],
    [24.7, { type: "settle" }],
  ];
  let s = START;
  return script.map(([at, action]) => {
    const next = step(s, action);
    const log = formatLog(logFor(s, action, next));
    s = next;
    return { at, action, log };
  });
})();

/* ------------------------------------------------------------------- store */

export interface DeckStore {
  get(): DeckState;
  subscribe(fn: () => void): () => void;
  dispatch(a: Action): void;
}
export interface ResettableStore extends DeckStore {
  /** Puts the state back without it being an action the log would describe. */
  reset(s: DeckState): void;
}

const same = (a: DeckState, b: DeckState) => a === b || JSON.stringify(a) === JSON.stringify(b);

export function createStore(initial: DeckState): ResettableStore {
  let state = initial;
  const subs = new Set<() => void>();
  const set = (next: DeckState) => {
    if (same(state, next)) return;
    state = next;
    for (const fn of [...subs]) fn();
  };
  return {
    get: () => state,
    subscribe(fn) {
      subs.add(fn);
      return () => {
        subs.delete(fn);
      };
    },
    dispatch: (a) => set(step(state, a)),
    reset: (s) => set(s),
  };
}
