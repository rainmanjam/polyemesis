import { describe, expect, it } from "vitest";
import { failoverElapsedSeconds, failoverBannerTone, failoverSinceClock } from "./failoverBanner";

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
  it("renders the switch as a local clock time", () => {
    const at = new Date(2026, 9, 29, 20, 31, 0); // local time, so the test is zone-independent
    const got = failoverSinceClock(at.toISOString());
    expect(got).toMatch(/20:31|8:31/);
  });

  it("is null for the zero time, so no 'since' is claimed", () => {
    expect(failoverSinceClock("0001-01-01T00:00:00Z")).toBeNull();
  });
});
