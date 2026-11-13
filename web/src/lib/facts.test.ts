import { afterEach, describe, expect, it, vi } from "vitest";
import facts from "../data/facts.json";

afterEach(() => {
  vi.doUnmock("../data/facts.json");
  vi.resetModules();
});

describe("facts", () => {
  it("words each server constant the way the pages state it", async () => {
    const f = await import("../data/facts.mjs");
    expect(f.MAX_TRACKS).toBe(facts.maxTracks);
    expect(f.FFMPEG_FLOOR).toBe(`${facts.ffmpegMinMajor}.0`); // a major version, written as the pages say "8.0 or later"
    expect(f.GO_FLOOR).toBe(facts.goMinVersion);
    expect(f.NODE_BUILD_FLOOR).toMatch(/^\d+\.\d+\+\/\d+\.\d+\+$/);
  });

  it("words a caret range and an open range as the two floors, whatever their numbers", async () => {
    vi.doMock("../data/facts.json", () => ({ default: { ...facts, uiNodeEngines: "^18.4.0 || >=20.1.0" } }));
    expect((await import("../data/facts.mjs")).NODE_BUILD_FLOOR).toBe("18.4+/20.1+");
  });

  it("a Node range of another shape fails the build instead of putting a different sentence on a page", async () => {
    vi.doMock("../data/facts.json", () => ({ default: { ...facts, uiNodeEngines: ">=22" } }));
    await expect(import("../data/facts.mjs")).rejects.toThrow(/cannot word the Node range ">=22"/);
  });
});
