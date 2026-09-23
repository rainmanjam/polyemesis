import { describe, expect, it } from "vitest";
import en from "@/lib/i18n/en.json";
import { tokenNotEnforcedKey } from "@/lib/tokenNotice";

describe("tokenNotEnforcedKey", () => {
  // A source created from its name alone has no ingest chosen, and the shared
  // SRT port refuses its token. The page told that operator their token was
  // "stored but not enforced" and to "turn on one-port ingest" -- a setting that
  // no longer exists -- as if publishing would work.
  it("tells an unchosen source that nothing is accepted until a protocol is picked", () => {
    expect(tokenNotEnforcedKey("")).toBe("sources.tokenUnusedUnset");
    expect(tokenNotEnforcedKey(undefined)).toBe("sources.tokenUnusedUnset");
  });

  it("tells a pull source the token is unused", () => {
    expect(tokenNotEnforcedKey("pull")).toBe("sources.tokenUnusedPull");
  });

  it("keeps the per-protocol copy for a listener that is not bound", () => {
    expect(tokenNotEnforcedKey("srt")).toBe("sources.tokenNotEnforcedSrt");
    expect(tokenNotEnforcedKey("rtmp")).toBe("sources.tokenNotEnforcedRtmp");
  });

  // The retired advice must not survive in any of the four explanations.
  it("never points at a one-port setting that no longer exists", () => {
    for (const mode of ["", "pull", "srt", "rtmp"] as const) {
      const text = (en as Record<string, string>)[tokenNotEnforcedKey(mode)];
      expect(text).toBeTruthy();
      expect(text).not.toMatch(/one-port|kept apart by port/i);
    }
  });
});
