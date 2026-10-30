// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import { readVodStash, writeVodStash } from "./vodStash";
import type { DestinationId, RoutingProfile } from "./types";

/* The switched-off second mix survives a reload in the same tab, and is never
 * offered on another destination (A14). */
const p: RoutingProfile = { mode: "simple", tracks: [{ track: 1, enabled: true, gain: 0.5 }], matrix: null, normalize: "auto", sampleRate: 48000 };
const a = 1 as DestinationId;
const b = 2 as DestinationId;

describe("vodStash", () => {
  it("keeps a stash per destination and clears it", () => {
    writeVodStash(a, p);
    expect(readVodStash(a)).toEqual(p);
    expect(readVodStash(b)).toBeNull();
    writeVodStash(a, null);
    expect(readVodStash(a)).toBeNull();
  });

  it("reads a corrupt entry as no stash", () => {
    sessionStorage.setItem("polyemesis.vodStash.1", "{not json");
    expect(readVodStash(a)).toBeNull();
  });
});
