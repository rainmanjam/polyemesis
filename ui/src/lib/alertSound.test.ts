import { afterEach, describe, expect, it, vi } from "vitest";
import { playAlertSound, resetAlertAudio, SOUND_PATTERNS } from "./alertSound";

interface FakeNode { connect: ReturnType<typeof vi.fn>; start?: ReturnType<typeof vi.fn>; stop?: ReturnType<typeof vi.fn>; [k: string]: unknown }

function fakeContext(state: "running" | "suspended" | "closed" = "running", resume?: () => Promise<void>) {
  const made = { osc: 0, gain: 0 };
  class Ctx {
    state = state;
    currentTime = 0;
    destination = {};
    createOscillator() {
      made.osc++;
      return { type: "sine", frequency: { value: 0 }, connect: vi.fn(), start: vi.fn(), stop: vi.fn() } as FakeNode;
    }
    createGain() {
      made.gain++;
      return {
        gain: { setValueAtTime: vi.fn(), linearRampToValueAtTime: vi.fn(), exponentialRampToValueAtTime: vi.fn() },
        connect: vi.fn(),
      };
    }
    resume = resume ?? (async () => { this.state = "running"; });
  }
  return { Ctx, made };
}

afterEach(() => {
  vi.unstubAllGlobals();
  resetAlertAudio();
});

describe("alert sounds", () => {
  it("are short bounded note patterns, no files and no URLs", () => {
    for (const notes of Object.values(SOUND_PATTERNS)) {
      expect(notes.length).toBeGreaterThan(0);
      for (const n of notes) {
        expect(n.at + n.len).toBeLessThan(1.5);
        expect(n.gain).toBeLessThanOrEqual(0.3);
      }
    }
  });

  it("plays nothing for none", async () => {
    const { Ctx, made } = fakeContext();
    vi.stubGlobal("AudioContext", Ctx);
    expect(await playAlertSound("none")).toBe(false);
    expect(made.osc).toBe(0);
  });

  it("schedules each note of the pattern", async () => {
    const { Ctx, made } = fakeContext();
    vi.stubGlobal("AudioContext", Ctx);
    expect(await playAlertSound("chime")).toBe(true);
    expect(made.osc).toBe(SOUND_PATTERNS.chime.length);
    expect(made.gain).toBe(SOUND_PATTERNS.chime.length);
  });

  it("resumes a suspended context before playing", async () => {
    const resume = vi.fn(async () => {});
    const { Ctx } = fakeContext("suspended", resume);
    vi.stubGlobal("AudioContext", Ctx);
    expect(await playAlertSound("pop")).toBe(true);
    expect(resume).toHaveBeenCalled();
  });

  it("does not throw when there is no AudioContext", async () => {
    vi.stubGlobal("AudioContext", undefined);
    vi.stubGlobal("webkitAudioContext", undefined);
    await expect(playAlertSound("bell")).resolves.toBe(false);
  });

  it("does not throw when the context cannot be built, will not resume, or is closed", async () => {
    vi.stubGlobal("AudioContext", class { constructor() { throw new Error("blocked"); } });
    await expect(playAlertSound("bell")).resolves.toBe(false);
    resetAlertAudio();
    const { Ctx } = fakeContext("suspended", async () => { throw new Error("no gesture"); });
    vi.stubGlobal("AudioContext", Ctx);
    await expect(playAlertSound("bell")).resolves.toBe(false);
    resetAlertAudio();
    vi.stubGlobal("AudioContext", fakeContext("closed").Ctx);
    await expect(playAlertSound("bell")).resolves.toBe(false);
  });

  it("falls back to the prefixed constructor", async () => {
    const { Ctx, made } = fakeContext();
    vi.stubGlobal("AudioContext", undefined);
    vi.stubGlobal("webkitAudioContext", Ctx);
    expect(await playAlertSound("pop")).toBe(true);
    expect(made.osc).toBe(1);
  });
});
