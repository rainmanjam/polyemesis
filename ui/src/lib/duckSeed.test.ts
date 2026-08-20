import { describe, expect, it } from "vitest";

import { duckSeed } from "./duckSeed";
import type { TrackAnnotation } from "./types";

const ann = (track: number, role: string): TrackAnnotation =>
  ({ track, role }) as TrackAnnotation;

describe("duckSeed", () => {
  it("seeds a duck whose trigger is not in the mix", () => {
    // THE CASE THE OLD GUARD REFUSED. A destination that excludes the mic role
    // has ONE track in its mix, and the old card returned null below two --
    // hiding the whole Ducking card, switch included. duckGraph() in
    // internal/routing/filtergraph.go deliberately keeps an out-of-mix trigger:
    // "that is how a feed which excludes the mic can still duck its music when
    // the host speaks".
    expect(duckSeed([1], [0, 1], [ann(0, "mic"), ann(1, "music")])).toEqual({
      trigger: [0],
      target: [1],
    });
  });

  it("seeds from the roles when they are annotated", () => {
    expect(duckSeed([1, 2], [0, 1, 2], [ann(0, "commentary"), ann(2, "game")])).toEqual({
      trigger: [0],
      target: [2],
    });
  });

  it("falls back to the first mixed track and the first ingest track beside it", () => {
    expect(duckSeed([0, 1], [0, 1], [])).toEqual({ trigger: [1], target: [0] });
  });

  it("keeps trigger and target disjoint, which the compiler requires", () => {
    // The only annotated mic is also the only mixed track, so it cannot be both
    // sides of the duck.
    const seed = duckSeed([0], [0, 1], [ann(0, "mic")]);
    expect(seed).not.toBeNull();
    expect(seed!.trigger.some((t) => seed!.target.includes(t))).toBe(false);
  });

  it("refuses when nothing in the mix could be pushed down", () => {
    expect(duckSeed([], [0, 1], [ann(0, "mic")])).toBeNull();
  });

  it("refuses when the ingest has no track left to trigger on", () => {
    expect(duckSeed([0], [0], [])).toBeNull();
  });
});
