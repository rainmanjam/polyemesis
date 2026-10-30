import type { StopImpact } from "./model.js";

// Hold-to-confirm for stop keys. A stop on YouTube ends the broadcast for good,
// so the key does three things a single tap cannot:
//
//   1. It loads the count of broadcasts the stop would end, FRESH, on press.
//      If that load fails the stop is refused. A stop with no count on the face
//      is a stop the operator did not see.
//   2. It arms a timer for the hold. Releasing before the timer fires cancels.
//   3. Only a press held for the whole duration calls onConfirm, once.
//
// The clock and the impact loader are injected, so the tests drive time
// directly and never sleep.

export type HoldPhase = "idle" | "loading" | "arming" | "fired" | "refused" | "cancelled";

/** Schedules fn after ms and returns a cancel function. */
export type Scheduler = (fn: () => void, ms: number) => () => void;

export interface PressResult {
  phase: HoldPhase;
  impact?: StopImpact;
  error?: unknown;
}

export class HoldToConfirm {
  private phase: HoldPhase = "idle";
  private cancelTimer: (() => void) | undefined;
  /** Bumped on every press and release, so a late impact answer cannot re-arm a released key. */
  private generation = 0;

  constructor(
    private readonly holdMs: number,
    private readonly schedule: Scheduler,
    private readonly onConfirm: () => void,
    private readonly onPhase: (phase: HoldPhase) => void = () => {},
  ) {}

  get current(): HoldPhase {
    return this.phase;
  }

  /** Key down. Loads the impact, then arms the hold if the key is still down. */
  async press(loadImpact: () => Promise<StopImpact>): Promise<PressResult> {
    if (this.phase === "loading" || this.phase === "arming") return { phase: this.phase };
    const gen = ++this.generation;
    this.set("loading");

    let impact: StopImpact;
    try {
      impact = await loadImpact();
    } catch (error) {
      if (gen !== this.generation) return { phase: this.phase };
      this.set("refused");
      return { phase: "refused", error };
    }
    // Released while the count was in flight: do not arm a key nobody holds.
    if (gen !== this.generation) return { phase: this.phase };

    this.set("arming");
    this.cancelTimer = this.schedule(() => this.fire(gen), this.holdMs);
    return { phase: "arming", impact };
  }

  /** Key up, or the key leaving the screen. Cancels any pending confirmation. */
  release(): void {
    this.generation++;
    this.cancelTimer?.();
    this.cancelTimer = undefined;
    if (this.phase === "loading" || this.phase === "arming") {
      this.set("cancelled");
    } else if (this.phase === "fired" || this.phase === "refused" || this.phase === "cancelled") {
      this.set("idle");
    }
  }

  private fire(gen: number): void {
    if (gen !== this.generation || this.phase !== "arming") return;
    this.cancelTimer = undefined;
    this.set("fired");
    this.onConfirm();
  }

  private set(phase: HoldPhase): void {
    this.phase = phase;
    this.onPhase(phase);
  }
}
