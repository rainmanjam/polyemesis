import { describe, expect, it } from "vitest";
import { setupChecklist } from "./setupChecklist";

const stateOf = (r: ReturnType<typeof setupChecklist>, id: number) =>
  r!.steps.find((s) => s.id === id)!.state;

describe("setupChecklist", () => {
  it("draws nothing while the source count is unknown", () => {
    // null is "the count has not arrived". Ticking step 1 on a guess would tell
    // an operator a source exists before the server has said so.
    expect(setupChecklist({ sourceCount: null, signal: false, destinations: 0 })).toBeNull();
  });

  it("ticks step 1 from the source count and nothing else", () => {
    const r = setupChecklist({ sourceCount: 1, signal: false, destinations: 0 });
    expect(stateOf(r, 1)).toBe("done");
    expect(r!.done).toBe(1);
  });

  it("makes the encoder step the next action once a source exists and no signal has arrived", () => {
    const r = setupChecklist({ sourceCount: 1, signal: false, destinations: 0 });
    expect(stateOf(r, 2)).toBe("next");
    expect(stateOf(r, 3)).toBe("waiting");
  });

  it("ticks the encoder and signal steps together, from the live signal", () => {
    const r = setupChecklist({ sourceCount: 1, signal: true, destinations: 0 });
    expect(stateOf(r, 2)).toBe("done");
    expect(stateOf(r, 3)).toBe("done");
    expect(r!.done).toBe(3);
  });

  it("never ticks signal or encoder without a source", () => {
    // With no source there are no status events to trust, so a stale snapshot
    // must not tick these.
    const r = setupChecklist({ sourceCount: 0, signal: true, destinations: 0 });
    expect(stateOf(r, 2)).not.toBe("done");
    expect(stateOf(r, 3)).not.toBe("done");
  });

  it("keeps Go live locked and names the one missing thing, or both", () => {
    expect(setupChecklist({ sourceCount: 1, signal: true, destinations: 0 })!.reason).toBe(
      "needDestination",
    );
    expect(setupChecklist({ sourceCount: 0, signal: false, destinations: 0 })!.reason).toBe(
      "needSourceAndDestination",
    );
  });

  it("unlocks Go live once a destination exists", () => {
    const r = setupChecklist({ sourceCount: 1, signal: true, destinations: 2 });
    expect(stateOf(r, 5)).toBe("next");
    expect(r!.reason).toBeNull();
  });
});
