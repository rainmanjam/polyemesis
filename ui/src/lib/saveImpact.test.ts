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

describe("isLevelOnlySave also compares the profiles", () => {
  const prof = (over: Record<string, unknown> = {}) => ({
    mode: "simple",
    tracks: [
      { track: 0, enabled: true, gain: 1 },
      { track: 1, enabled: true, gain: 1, muted: false },
    ],
    delayMs: 0,
    loudness: null,
    ...over,
  });
  const g = graph("1", "1");

  it("a gain or mute alone is level-only", () => {
    const draft = prof({
      tracks: [
        { track: 0, enabled: true, gain: 0.5 },
        { track: 1, enabled: true, gain: 1, muted: true },
      ],
    });
    expect(
      isLevelOnlySave({ storedGraph: g, draftGraph: g, secondMixChanged: false, storedProfile: prof(), draftProfile: draft }),
    ).toBe(true);
  });

  // A negative delay holds the VIDEO back in the output arguments; the audio
  // graph does not move, so the graph check alone would call it live.
  // MUTATION: drop the profile comparison. Observed: FAIL.
  it("a negative delay restarts though the audio graph is unchanged", () => {
    expect(
      isLevelOnlySave({
        storedGraph: g,
        draftGraph: g,
        secondMixChanged: false,
        storedProfile: prof({ delayMs: -100 }),
        draftProfile: prof({ delayMs: -200 }),
      }),
    ).toBe(false);
  });

  it("null, absent and empty read alike", () => {
    expect(
      isLevelOnlySave({
        storedGraph: g,
        draftGraph: g,
        secondMixChanged: false,
        storedProfile: prof({ excludeRoles: null }),
        draftProfile: prof({ excludeRoles: [], loudness: undefined }),
      }),
    ).toBe(true);
  });
});
