import { describe, expect, it } from "vitest";
import { isLevelOnlySave, maskLiveVolumes } from "./saveImpact";

const graph = (g0: string, g1: string, extra = "") =>
  `[0:a:0]pan=stereo|c0=1*c0|c1=1*c1,volume@trk0=volume=${g0}[a_t0];` +
  `[0:a:1]pan=stereo|c0=1*c0|c1=1*c1,volume@trk1=volume=${g1}[a_t1];` +
  `[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0${extra}[aout]`;

describe("maskLiveVolumes", () => {
  it("blanks every named level and nothing else", () => {
    expect(maskLiveVolumes(graph("1", "0.5"))).toBe(maskLiveVolumes(graph("0", "1.25")));
    expect(maskLiveVolumes(graph("1", "1"))).toContain("pan=stereo|c0=1*c0");
  });
});

describe("isLevelOnlySave", () => {
  it("a gain or mute alone applies live", () => {
    expect(
      isLevelOnlySave({ storedGraph: graph("1", "1"), draftGraph: graph("0", "0.7"), secondMixChanged: false }),
    ).toBe(true);
  });

  it("anything beyond a level restarts", () => {
    expect(
      isLevelOnlySave({
        storedGraph: graph("1", "1"),
        draftGraph: graph("1", "1", ",alimiter=limit=0.97"),
        secondMixChanged: false,
      }),
    ).toBe(false);
  });

  it("an unknown or stale draft compile is answered conservatively", () => {
    expect(isLevelOnlySave({ storedGraph: graph("1", "1"), draftGraph: null, secondMixChanged: false })).toBe(false);
    expect(isLevelOnlySave({ storedGraph: null, draftGraph: graph("1", "1"), secondMixChanged: false })).toBe(false);
  });

  it("a changed second mix counts as a restart", () => {
    expect(
      isLevelOnlySave({ storedGraph: graph("1", "1"), draftGraph: graph("0", "1"), secondMixChanged: true }),
    ).toBe(false);
  });
});
