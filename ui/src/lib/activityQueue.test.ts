import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  advanceAlerts,
  displayMs,
  emptyAlertQueue,
  enqueueAlert,
  FOLLOW_RAID_LIMIT,
  GAP_MS,
  nextAlertDelay,
  QUEUE_CAP,
  type AlertQueueState,
} from "./activityQueue";
import type { ActivityDisplay, ActivityKind } from "./types";

const ALL = ["follow", "subscribe", "resub", "gift", "tip", "raid", "redemption", "hypetrain", "shoutout"];
const style = { kinds: ALL, seconds: 6 };

let n = 0;
const ev = (kind: ActivityKind, over: Partial<ActivityDisplay> = {}): ActivityDisplay => ({
  id: `a${++n}`,
  platform: "twitch",
  kind,
  name: `n${n}`,
  at: "2026-11-07T20:00:00Z",
  ...over,
});

/** A page's loop in miniature: the clock is vitest's fake Date, and a timer
 *  fires advanceAlerts after nextAlertDelay, exactly as OverlayPage does. */
function drive(initial: AlertQueueState = emptyAlertQueue()) {
  const box = { state: initial, shown: [] as string[] };
  let timer: ReturnType<typeof setTimeout> | undefined;
  const tick = () => {
    box.state = advanceAlerts(box.state, style, Date.now());
    if (box.state.current && box.shown.at(-1) !== box.state.current.display.id) {
      box.shown.push(box.state.current.display.id);
    }
    const d = nextAlertDelay(box.state, style, Date.now());
    clearTimeout(timer);
    if (d !== null) timer = setTimeout(tick, d);
  };
  return {
    box,
    push(d: ActivityDisplay) {
      box.state = enqueueAlert(box.state, d, style, Date.now());
      tick();
    },
  };
}

describe("activity alert queue", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(1_000_000);
    n = 0;
  });
  afterEach(() => vi.useRealTimers());

  it("shows one alert at a time, in order, each for style.seconds", () => {
    const d = drive();
    const a = ev("tip");
    const b = ev("subscribe");
    d.push(a);
    d.push(b);
    expect(d.box.state.current?.display.id).toBe(a.id);
    vi.advanceTimersByTime(5_999);
    expect(d.box.state.current?.display.id).toBe(a.id);
    vi.advanceTimersByTime(1);
    expect(d.box.state.current).toBeNull(); // the gap, so the exit can play
    vi.advanceTimersByTime(GAP_MS);
    expect(d.box.state.current?.display.id).toBe(b.id);
    vi.advanceTimersByTime(6_000 + GAP_MS);
    expect(d.box.state.current).toBeNull();
    expect(d.box.shown).toEqual([a.id, b.id]);
    expect(nextAlertDelay(d.box.state, style, Date.now())).toBeNull();
  });

  it("starts at once when idle", () => {
    const d = drive();
    const a = ev("follow");
    d.push(a);
    expect(d.box.state.current?.display.id).toBe(a.id);
  });

  it("clamps the display time to 3..15 seconds", () => {
    expect(displayMs({ seconds: 1 })).toBe(3000);
    expect(displayMs({ seconds: 99 })).toBe(15000);
    expect(displayMs({ seconds: 8 })).toBe(8000);
    expect(displayMs({ seconds: Number.NaN })).toBe(6000);
  });

  it("ignores kinds the style does not list", () => {
    const s0 = emptyAlertQueue();
    const s1 = enqueueAlert(s0, ev("raid"), { kinds: ["tip"] }, 0);
    expect(s1).toBe(s0);
  });

  it("dedupes by id, including an alert already shown", () => {
    const d = drive();
    const a = ev("tip");
    d.push(a);
    d.push({ ...a });
    vi.advanceTimersByTime(60_000);
    d.push({ ...a });
    expect(d.box.shown).toEqual([a.id]);
  });

  it("collapses more than five follows in ten seconds into one item", () => {
    const d = drive();
    d.push(ev("tip")); // occupies the screen so follows queue
    for (let i = 0; i < FOLLOW_RAID_LIMIT; i++) d.push(ev("follow"));
    expect(d.box.state.queue.filter((q) => q.followers)).toHaveLength(0);
    expect(d.box.state.queue).toHaveLength(FOLLOW_RAID_LIMIT);
    d.push(ev("follow")); // the sixth
    const q = d.box.state.queue;
    expect(q).toHaveLength(1);
    expect(q[0].followers).toBe(6);
    expect(q[0].display.name).toBe("");
    expect(q[0].display.count).toBe(6);
  });

  it("keeps counting a queued raid item", () => {
    const d = drive();
    d.push(ev("tip"));
    for (let i = 0; i < 6; i++) d.push(ev("follow"));
    vi.advanceTimersByTime(2_000);
    d.push(ev("follow"));
    d.push(ev("follow"));
    expect(d.box.state.queue).toHaveLength(1);
    expect(d.box.state.queue[0].followers).toBe(8);
  });

  it("does not collapse follows that are spread over time", () => {
    const d = drive();
    d.push(ev("tip"));
    for (let i = 0; i < 8; i++) {
      d.push(ev("follow"));
      vi.advanceTimersByTime(2_500); // 4 per 10 s
    }
    expect(d.box.state.queue.some((q) => q.followers)).toBe(false);
  });

  it("keeps other kinds in place when follows collapse", () => {
    const d = drive();
    d.push(ev("tip"));
    const sub = ev("subscribe");
    d.push(ev("follow"));
    d.push(sub);
    for (let i = 0; i < 5; i++) d.push(ev("follow"));
    const kinds = d.box.state.queue.map((q) => q.display.kind);
    expect(kinds).toEqual(["follow", "subscribe"]);
  });

  it("sheds the oldest follow first when over the cap, never a tip", () => {
    let s = emptyAlertQueue();
    let t = 0;
    // Spread follows over time so the raid rule does not merge them.
    const add = (d: ActivityDisplay) => {
      t += 11_000;
      s = enqueueAlert(s, d, style, t);
    };
    const tip = ev("tip");
    add(tip);
    const follow = ev("follow");
    add(follow);
    for (let i = 0; i < QUEUE_CAP - 2; i++) add(ev("subscribe"));
    expect(s.queue).toHaveLength(QUEUE_CAP);
    add(ev("tip"));
    expect(s.queue).toHaveLength(QUEUE_CAP);
    expect(s.queue.some((q) => q.display.id === follow.id)).toBe(false);
    expect(s.queue.some((q) => q.display.id === tip.id)).toBe(true);
  });

  it("then sheds the oldest non-tip, and lets tips exceed the cap", () => {
    let s = emptyAlertQueue();
    let t = 0;
    const add = (d: ActivityDisplay) => {
      t += 11_000;
      s = enqueueAlert(s, d, style, t);
    };
    const sub = ev("subscribe");
    add(sub);
    for (let i = 0; i < QUEUE_CAP - 1; i++) add(ev("tip"));
    add(ev("tip"));
    // The subscribe was the only thing that could go.
    expect(s.queue).toHaveLength(QUEUE_CAP);
    expect(s.queue.some((q) => q.display.id === sub.id)).toBe(false);
    add(ev("tip"));
    expect(s.queue).toHaveLength(QUEUE_CAP + 1);
    expect(s.queue.every((q) => q.display.kind === "tip")).toBe(true);
  });

  it("drops the arrival itself when it is the only non-tip over the cap", () => {
    let s = emptyAlertQueue();
    let t = 0;
    for (let i = 0; i < QUEUE_CAP; i++) {
      t += 11_000;
      s = enqueueAlert(s, ev("tip"), style, t);
    }
    s = enqueueAlert(s, ev("follow"), style, t + 11_000);
    expect(s.queue).toHaveLength(QUEUE_CAP);
    expect(s.queue.every((q) => q.display.kind === "tip")).toBe(true);
  });

  it("follows a style change in seconds on the next advance", () => {
    let s = enqueueAlert(emptyAlertQueue(), ev("tip"), style, 0);
    s = advanceAlerts(s, style, 0);
    expect(nextAlertDelay(s, { seconds: 10 }, 0)).toBe(10_000);
    expect(advanceAlerts(s, { seconds: 10 }, 7_000).current).not.toBeNull();
    expect(advanceAlerts(s, { seconds: 3 }, 3_000).current).toBeNull();
  });

  it("waits out the gap, and starts at once after a long stall", () => {
    let s = emptyAlertQueue();
    s = enqueueAlert(s, ev("tip"), style, 0);
    s = enqueueAlert(s, ev("tip"), style, 0);
    s = advanceAlerts(s, style, 0);
    s = advanceAlerts(s, style, 6_000);
    expect(s.current).toBeNull();
    expect(nextAlertDelay(s, style, 6_000)).toBe(GAP_MS);
    s = advanceAlerts(s, style, 6_000 + GAP_MS - 1);
    expect(s.current).toBeNull();
    s = advanceAlerts(s, style, 6_000 + GAP_MS);
    expect(s.current).not.toBeNull();

    // A throttled tab wakes long after both: the next one is not delayed more.
    let t = enqueueAlert(emptyAlertQueue(), ev("tip"), style, 0);
    t = enqueueAlert(t, ev("tip"), style, 0);
    t = advanceAlerts(t, style, 0);
    t = advanceAlerts(t, style, 60_000);
    expect(t.current?.display.id).toBeDefined();
    expect(t.shownAt).toBe(60_000);
  });
});
