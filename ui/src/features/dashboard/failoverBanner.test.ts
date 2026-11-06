import { describe, expect, it } from "vitest";
import { failoverElapsedSeconds, failoverBannerTone, failoverSinceClock } from "./failoverBanner";
import { setDisplayTimeZone } from "@/lib/format";

describe("failoverElapsedSeconds", () => {
  const now = Date.parse("2026-10-29T20:33:14Z");

  it("counts whole seconds since the switch", () => {
    expect(failoverElapsedSeconds("2026-10-29T20:31:00Z", now)).toBe(134);
  });

  it("returns null for the zero time the server sends before any switch", () => {
    expect(failoverElapsedSeconds("0001-01-01T00:00:00Z", now)).toBeNull();
  });

  it("returns null for an empty or unparseable stamp", () => {
    expect(failoverElapsedSeconds("", now)).toBeNull();
    expect(failoverElapsedSeconds("not a time", now)).toBeNull();
  });

  it("clamps a switch stamped in the future (clock skew) to zero", () => {
    expect(failoverElapsedSeconds("2026-10-29T20:40:00Z", now)).toBe(0);
  });
});

describe("failoverBannerTone", () => {
  it("is amber for the backup and the playlist, red for the slate", () => {
    expect(failoverBannerTone("backup")).toBe("warn");
    expect(failoverBannerTone("playlist")).toBe("warn");
    expect(failoverBannerTone("slate")).toBe("down");
  });

  it("is null on the primary, where no banner is drawn", () => {
    expect(failoverBannerTone("primary")).toBeNull();
  });
});

describe("failoverSinceClock", () => {
  it("renders the switch as a clock time in the console's display zone", () => {
    setDisplayTimeZone("Australia/Sydney");
    try {
      // 09:31Z is 20:31 in Sydney (AEDT) on 29 October.
      expect(failoverSinceClock("2026-10-29T09:31:00Z")).toBe("20:31");
    } finally {
      setDisplayTimeZone("UTC");
    }
  });

  it("is null for the zero time, so no 'since' is claimed", () => {
    expect(failoverSinceClock("0001-01-01T00:00:00Z")).toBeNull();
  });
});
