import { describe, expect, it } from "vitest";
import { outputState, routingDropsSignal, sourcePeak, type OutputReport } from "./outputStrip";

const NOW = Date.parse("2026-10-29T12:00:00Z");
const report = (over: Partial<OutputReport> = {}): OutputReport => ({
  destinationId: 7,
  seconds: 30,
  momentaryLufs: -16,
  truePeakDbtp: -1.5,
  reason: "",
  at: new Date(NOW - 1000).toISOString(),
  ...over,
});

describe("outputState", () => {
  it("names every way the output can be unmeasured", () => {
    expect(outputState(null, 7, NOW, NOW).kind).toBe("off");
    expect(outputState({ enabled: false, reports: [report()] }, 7, NOW, NOW).kind).toBe("off");
    expect(outputState({ enabled: true, reports: [] }, 7, NOW, NOW).kind).toBe("absent");
    expect(outputState({ enabled: true, reports: [report({ seconds: 0 })] }, 7, NOW, NOW).kind).toBe("starting");
    expect(outputState({ enabled: true, reports: [report({ error: "spawn failed" })] }, 7, NOW, NOW)).toEqual({
      kind: "error",
      error: "spawn failed",
    });
  });

  it("a report that has not changed for the server's stale window is history, not live", () => {
    const feed = { enabled: true, reports: [report()] };
    expect(outputState(feed, 7, NOW, NOW - 11_000).kind).toBe("stale");
    expect(outputState(feed, 7, NOW, NOW - 1_000).kind).toBe("live");
  });

  it("judges staleness on the browser's clock alone, whatever the server's says", () => {
    // A server clock an hour behind must not make a fresh frame read as stale.
    const skewed = report({ at: new Date(NOW - 3_600_000).toISOString() });
    expect(outputState({ enabled: true, reports: [skewed] }, 7, NOW, NOW).kind).toBe("live");
  });

  it("reads only this destination's report", () => {
    expect(outputState({ enabled: true, reports: [report({ destinationId: 8 })] }, 7, NOW, NOW).kind).toBe("absent");
  });
});

describe("sourcePeak", () => {
  it("takes the loudest channel of the tracks this destination sends", () => {
    expect(sourcePeak([[-30, -20], [-6, -8], [-1, -1]], [0, 1])).toBe(-6);
    expect(sourcePeak([[-30, -20]], [3])).toBeNull();
    expect(sourcePeak(undefined, [0])).toBeNull();
  });
});

describe("routingDropsSignal", () => {
  const live = (tp: number, lufs = -70) =>
    outputState({ enabled: true, reports: [report({ truePeakDbtp: tp, momentaryLufs: lufs })] }, 7, NOW, NOW);

  it("flags a hot source with a silent output", () => {
    expect(routingDropsSignal(live(-120), -12)).toBe(true);
  });

  it("does not call a quiet-but-present output silent because loudness sits at the gate", () => {
    expect(routingDropsSignal(live(-40, -70), -12)).toBe(false);
  });

  it("says nothing without a live report or a source reading", () => {
    const stale = outputState({ enabled: true, reports: [report({ truePeakDbtp: -120 })] }, 7, NOW, NOW - 60_000);
    expect(routingDropsSignal(stale, -12)).toBe(false);
    expect(routingDropsSignal(live(-120), null)).toBe(false);
    expect(routingDropsSignal(live(-120), -70)).toBe(false);
  });
});
