import { describe, expect, it } from "vitest";

import { storedMixes } from "./storedMixes";
import type { RoutingProfile } from "@/lib/types";

/* THE DISCARD RESTORES THE STORED MIX RULE (A7). Discard must put back exactly
 * what the server holds, as independent copies: an edit made after a discard
 * must not reach back into the stored value the next discard will read. */

const profile = { normalize: "auto", delayMs: 0, exclude: [1] } as unknown as RoutingProfile;
const second = { normalize: "off", delayMs: 40, exclude: [] } as unknown as RoutingProfile;

describe("storedMixes", () => {
  it("returns the stored live and second mixes", () => {
    const got = storedMixes({ profile, vodProfile: second });
    expect(got.profile).toEqual(profile);
    expect(got.vodProfile).toEqual(second);
  });

  it("returns copies, so editing the result cannot change the stored value", () => {
    const stored = { profile, vodProfile: second };
    const got = storedMixes(stored);
    expect(got.profile).not.toBe(stored.profile);
    expect(got.vodProfile).not.toBe(stored.vodProfile);
  });

  it("normalises an absent second mix to null, not undefined", () => {
    // The server omits vodProfile entirely when there is no second mix.
    expect(storedMixes({ profile }).vodProfile).toBeNull();
    expect(storedMixes({ profile, vodProfile: null }).vodProfile).toBeNull();
  });
});
