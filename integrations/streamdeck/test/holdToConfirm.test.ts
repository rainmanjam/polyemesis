import { describe, expect, it, vi } from "vitest";
import { HoldToConfirm, type HoldPhase } from "../src/holdToConfirm.js";
import type { StopImpact } from "../src/model.js";

/** A scheduler the test advances by hand. Nothing sleeps. */
function manualClock() {
  const timers: { fn: () => void; ms: number; cancelled: boolean; ran: boolean }[] = [];
  return {
    schedule(fn: () => void, ms: number) {
      const t = { fn, ms, cancelled: false, ran: false };
      timers.push(t);
      return () => {
        t.cancelled = true;
      };
    },
    /** Fires every live timer, as if the full hold elapsed. */
    elapse() {
      for (const t of timers) {
        if (!t.cancelled && !t.ran) {
          t.ran = true;
          t.fn();
        }
      }
    },
    armed: () => timers.filter((t) => !t.cancelled && !t.ran).length,
    timers,
  };
}

const IMPACT: StopImpact = { destinations: 3, youtubeBroadcasts: 2 };
const resolved = (v: StopImpact) => () => Promise.resolve(v);
const rejected = () => () => Promise.reject(new Error("impact unavailable"));

function setup(holdMs = 1500) {
  const clock = manualClock();
  const onConfirm = vi.fn();
  const phases: HoldPhase[] = [];
  const hold = new HoldToConfirm(holdMs, clock.schedule, onConfirm, (p) => phases.push(p));
  return { clock, onConfirm, phases, hold };
}

describe("HoldToConfirm", () => {
  it("fires once when the key is held for the whole duration", async () => {
    const { clock, onConfirm, hold } = setup(1500);
    const press = await hold.press(resolved(IMPACT));
    expect(press).toEqual({ phase: "arming", impact: IMPACT });
    expect(clock.timers[0]!.ms).toBe(1500);

    clock.elapse();
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(hold.current).toBe("fired");
  });

  it("cancels on an early release, so the stop never fires", async () => {
    const { clock, onConfirm, hold } = setup();
    await hold.press(resolved(IMPACT));
    hold.release();

    clock.elapse();
    expect(onConfirm).not.toHaveBeenCalled();
    expect(hold.current).toBe("cancelled");
  });

  it("refuses the stop when the count cannot be loaded, and never arms a timer", async () => {
    const { clock, onConfirm, hold } = setup();
    const result = await hold.press(rejected());
    expect(result.phase).toBe("refused");
    expect(result.error).toBeInstanceOf(Error);
    expect(clock.armed()).toBe(0);

    clock.elapse();
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("does not arm a key that was released while the count was still loading", async () => {
    const { clock, onConfirm, hold } = setup();
    let answer!: (v: StopImpact) => void;
    const pending = hold.press(() => new Promise<StopImpact>((resolve) => (answer = resolve)));
    hold.release();
    answer(IMPACT);
    await pending;

    expect(clock.armed()).toBe(0);
    clock.elapse();
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("does not stack a second timer on a repeated key-down", async () => {
    const { clock, onConfirm, hold } = setup();
    await hold.press(resolved(IMPACT));
    await hold.press(resolved(IMPACT));
    expect(clock.armed()).toBe(1);
    clock.elapse();
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("can be pressed again after a cancelled hold", async () => {
    const { clock, onConfirm, hold } = setup();
    await hold.press(resolved(IMPACT));
    hold.release();
    await hold.press(resolved(IMPACT));
    clock.elapse();
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("reports each phase it passes through", async () => {
    const { hold, phases } = setup();
    await hold.press(resolved(IMPACT));
    hold.release();
    expect(phases).toEqual(["loading", "arming", "cancelled"]);
  });
});
