import { describe, expect, it } from "vitest";
import { WIRE } from "@/lib/contract/wire";
import { accentFor } from "@/lib/chat";
import { platformLabel } from "@/lib/audience";
import { platformName, X_GO_LIVE_TITLE_MAX } from "./platforms";

describe("platformName", () => {
  it("names every platform the registry knows, and none by its raw id", () => {
    for (const p of WIRE.platforms.platforms) {
      if (!("enum" in p)) continue;
      expect(platformName(p.enum)).toBe(p.shortName);
      expect(platformName(p.enum)).not.toBe(p.enum);
    }
  });

  it("names a retired platform, whose rows still exist", () => {
    expect(platformName("trovo")).toBe("Trovo");
  });

  it("shows an id it does not know as it came", () => {
    expect(platformName("mixer")).toBe("mixer");
  });

  it("is the one list every caller reads", () => {
    expect(platformLabel("x")).toBe("X");
    expect(accentFor("rumble").label).toBe("Rumble");
    expect(accentFor("youtube").label).toBe("YouTube");
  });

  it("serves the X go-live title limit from the server's rule", () => {
    expect(X_GO_LIVE_TITLE_MAX).toBe(WIRE.platforms.rules.xGoLiveTitleMax);
  });
});
