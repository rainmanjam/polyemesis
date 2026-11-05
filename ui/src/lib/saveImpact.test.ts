import { describe, expect, it } from "vitest";
import { maskLiveVolumes, saveImpact, savedToastKey, type ArgvRouting } from "./saveImpact";

const graph = (g0: string, g1: string, extra = "") =>
  `[0:a:0]pan=stereo|c0=1*c0|c1=1*c1,volume@trk0=volume=${g0}[a_t0];` +
  `[0:a:1]pan=stereo|c0=1*c0|c1=1*c1,volume@trk1=volume=${g1}[a_t1];` +
  `[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0${extra}[aout]`;

const compiled = (over: Partial<ArgvRouting> = {}): ArgvRouting => ({
  filterComplex: graph("1", "1"),
  outLabel: "aout",
  tracks: [0, 1],
  ...over,
});

describe("maskLiveVolumes", () => {
  it("blanks every named level and nothing else", () => {
    expect(maskLiveVolumes(graph("1", "0.5"))).toBe(maskLiveVolumes(graph("0", "1.25")));
    expect(maskLiveVolumes(graph("1", "1"))).toContain("pan=stereo|c0=1*c0");
  });
});

describe("saveImpact", () => {
  const stored = compiled();
  const impact = (draft: ArgvRouting | null | undefined, over: Partial<Parameters<typeof saveImpact>[0]> = {}) =>
    saveImpact({ stored, draft, secondMixChanged: false, ...over });

  it("a gain or mute alone applies live", () => {
    expect(impact(compiled({ filterComplex: graph("0", "0.7") }))).toBe("levels");
  });

  it("anything beyond a level restarts", () => {
    expect(impact(compiled({ filterComplex: graph("1", "1", ",alimiter=limit=0.97") }))).toBe("restart");
  });

  // E3: music rights switched on for a destination whose music track had
  // already left. The profile changed (excludeRoles), the compile did not, and
  // the engine kept the process. The page said "restarting" all the same.
  // MUTATION: answer "levels" whenever the masked graphs match. Observed: FAIL.
  it("a change that reaches nothing the destination sends restarts nothing", () => {
    expect(impact(compiled())).toBe("none");
  });

  // E1: every tick of a fader drag starts a compile; until it answers the page
  // does not know the cost, and must not read that as a restart.
  // MUTATION: return "restart" for an undefined draft. Observed: FAIL.
  it("says pending, not restart, while the current draft's compile is out", () => {
    expect(impact(undefined)).toBe("pending");
  });

  it("a failed compile, or no stored compile, is answered conservatively", () => {
    expect(impact(null)).toBe("restart");
    expect(saveImpact({ stored: null, draft: compiled(), secondMixChanged: false })).toBe("restart");
  });

  it("a changed second mix counts as a restart, compiled or not", () => {
    expect(impact(compiled({ filterComplex: graph("0", "1") }), { secondMixChanged: true })).toBe("restart");
    expect(impact(undefined, { secondMixChanged: true })).toBe("restart");
  });

  // Each of these reaches the argv outside the graph (engine destSpecFor), so
  // each restarts though the masked graph is unchanged.
  it("a negative delay restarts though the audio graph is unchanged", () => {
    // It holds the VIDEO back in the output arguments.
    expect(
      saveImpact({
        stored: compiled({ videoDelayMs: 100 }),
        draft: compiled({ videoDelayMs: 200 }),
        secondMixChanged: false,
      }),
    ).toBe("restart");
  });

  it("a sample rate, a track list or an output label restarts", () => {
    expect(impact(compiled(), { storedSampleRate: 48000, draftSampleRate: 44100 })).toBe("restart");
    expect(impact(compiled({ tracks: [0] }))).toBe("restart");
    expect(impact(compiled({ outLabel: "a_norm" }))).toBe("restart");
    expect(impact(compiled({ secondOutLabel: "avod" }))).toBe("restart");
  });

  it("absent, null and zero read alike", () => {
    expect(
      saveImpact({
        stored: compiled({ videoDelayMs: 0, tracks: null }),
        draft: compiled({ videoDelayMs: undefined, tracks: [] }),
        storedSampleRate: null,
        draftSampleRate: 0,
        secondMixChanged: false,
      }),
    ).toBe("none");
  });
});

describe("savedToastKey follows the server's answer", () => {
  it("a restart the server did says restarting", () => {
    expect(savedToastKey({ enabled: true, applied: "restart", reason: "x" })).toBe("route.savedRestarting");
  });

  it("a live answer with no reason is a level that reached the output", () => {
    expect(savedToastKey({ enabled: true, applied: "live" })).toBe("route.savedLive");
  });

  // E3: music rights on a mix the music track had already left.
  it("a live answer with a reason kept running without a level going out", () => {
    expect(savedToastKey({ enabled: true, applied: "live", reason: "nothing changed" })).toBe("route.savedKeptRunning");
  });

  it("no answer, or a disabled destination, claims nothing about a restart", () => {
    expect(savedToastKey({ enabled: true })).toBe("route.saved");
    expect(savedToastKey({ enabled: false, applied: "restart" })).toBe("route.saved");
  });
});
