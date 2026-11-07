/** The alerts overlay's pacing rules (design 2.7), kept free of React and of
 *  the clock so they are unit-tested directly.
 *
 *  One alert is on screen at a time, for style.seconds. Alerts that arrive
 *  meanwhile wait in order. Two things keep a bad minute from burying the good
 *  ones: a follow raid collapses into one "N new followers" item, and a full
 *  queue sheds follows first. A tip is never shed -- it is somebody's money.
 *
 *  Every function takes `now` (epoch ms) and returns a new state; nothing here
 *  reads Date.now or sets a timer, so a test drives time by passing numbers and
 *  the page drives it with a timeout from nextDelay. */

import { OVERLAY_ALERT_KINDS } from "./overlay";
import type { ActivityDisplay, AlertStyle } from "./types";

/** The most alerts held waiting. Tips may exceed it; see enqueueAlert. */
export const QUEUE_CAP = 50;
/** More than this many follows inside FOLLOW_WINDOW_MS is a raid. */
export const FOLLOW_RAID_LIMIT = 5;
export const FOLLOW_WINDOW_MS = 10_000;
/** The pause between two alerts, long enough for the exit transition. */
export const GAP_MS = 600;
/** How many recent ids are remembered for the dedupe. A reconnect replays
 *  under ten seconds of alerts, so this is generous. */
const SEEN_LIMIT = 200;

export interface QueuedAlert {
  display: ActivityDisplay;
  /** Set on a collapsed follow raid: how many followers the one item stands
   *  for. The page draws "N new followers" instead of a name. */
  followers?: number;
  /** When this client received the (first) alert, ms. */
  receivedAt: number;
}

export interface AlertQueueState {
  current: QueuedAlert | null;
  /** When `current` went on screen, ms. */
  shownAt: number;
  /** The earliest the next alert may go on screen (after the gap). */
  freeAt: number;
  queue: QueuedAlert[];
  /** Ids already accepted, oldest first, for the dedupe. */
  seen: string[];
  /** Arrival times of recent follows, for the raid rule. */
  follows: number[];
}

export function emptyAlertQueue(): AlertQueueState {
  return { current: null, shownAt: 0, freeAt: 0, queue: [], seen: [], follows: [] };
}

/** How long an alert stays up, in ms, from the style's seconds (clamped to the
 *  3..15 the server enforces, so a bad value cannot park an alert for ever). */
export function displayMs(style: Pick<AlertStyle, "seconds">): number {
  const s = Number.isFinite(style.seconds) ? style.seconds : 6;
  return Math.min(15, Math.max(3, s)) * 1000;
}

/** Adds an alert. Returns the same state when it is filtered out or a repeat. */
export function enqueueAlert(
  state: AlertQueueState,
  display: ActivityDisplay,
  style: { kinds?: AlertStyle["kinds"] | null },
  now: number,
): AlertQueueState {
  // kinds can arrive null/missing: fall back to every overlay kind.
  const kinds: readonly string[] = style.kinds ?? OVERLAY_ALERT_KINDS;
  if (!kinds.includes(display.kind)) return state;
  if (state.seen.includes(display.id)) return state;

  const seen = [...state.seen, display.id].slice(-SEEN_LIMIT);
  let queue = [...state.queue, { display, receivedAt: now }];
  let follows = state.follows;

  if (display.kind === "follow") {
    follows = [...state.follows, now].filter((t) => now - t <= FOLLOW_WINDOW_MS);
    if (follows.length > FOLLOW_RAID_LIMIT) queue = collapseFollows(queue);
  }
  return { ...state, queue: shed(queue), seen, follows };
}

/** Merges every waiting follow into one item at the place of the first. A
 *  queued collapsed item counts as the followers it stands for, which is how
 *  its count keeps rising while it waits. */
function collapseFollows(queue: QueuedAlert[]): QueuedAlert[] {
  const at = queue.findIndex((q) => q.display.kind === "follow");
  if (at < 0) return queue;
  let total = 0;
  for (const q of queue) if (q.display.kind === "follow") total += q.followers ?? 1;
  const first = queue[at];
  const merged: QueuedAlert = {
    // The id of the first follow, kept so a replayed duplicate of it is still
    // recognised; the name is dropped because it stands for many people.
    display: { ...first.display, name: "", count: total },
    followers: total,
    receivedAt: first.receivedAt,
  };
  const out: QueuedAlert[] = [];
  queue.forEach((q, i) => {
    if (i === at) out.push(merged);
    else if (q.display.kind !== "follow") out.push(q);
  });
  return out;
}

/** Trims the waiting list to QUEUE_CAP: the oldest follow goes first, then the
 *  oldest of any other kind but tip. A list of nothing but tips is left over
 *  the cap rather than lose one. */
function shed(queue: QueuedAlert[]): QueuedAlert[] {
  let q = queue;
  while (q.length > QUEUE_CAP) {
    let drop = q.findIndex((x) => x.display.kind === "follow");
    if (drop < 0) drop = q.findIndex((x) => x.display.kind !== "tip");
    if (drop < 0) break;
    q = q.filter((_, i) => i !== drop);
  }
  return q;
}

/** Moves the queue on: retires the alert whose time is up and puts the next on
 *  screen once the gap has passed. One step per call; call again after
 *  nextDelay. */
export function advanceAlerts(
  state: AlertQueueState,
  style: Pick<AlertStyle, "seconds">,
  now: number,
): AlertQueueState {
  let s = state;
  if (s.current && now >= s.shownAt + displayMs(style)) {
    s = { ...s, current: null, freeAt: s.shownAt + displayMs(style) + GAP_MS };
  }
  if (!s.current && s.queue.length > 0 && now >= s.freeAt) {
    const [next, ...rest] = s.queue;
    s = { ...s, current: next, shownAt: now, queue: rest };
  }
  return s;
}

/** Milliseconds until the state next needs advanceAlerts, or null when it is
 *  idle (nothing shown, nothing waiting) and only an arrival can change it. */
export function nextAlertDelay(
  state: AlertQueueState,
  style: Pick<AlertStyle, "seconds">,
  now: number,
): number | null {
  if (state.current) return Math.max(0, state.shownAt + displayMs(style) - now);
  if (state.queue.length > 0) return Math.max(0, state.freeAt - now);
  return null;
}
