import { describe, expect, it, vi } from "vitest";

import { TOUR_PAGE } from "../data/tour-page.mjs";
import { filmedRelease, latestRelease, parseLatestRelease, releaseOf } from "./release";

describe("parseLatestRelease", () => {
  it("returns the version and date of a release heading", () => {
    expect(parseLatestRelease("## [0.18.0] — 2026-11-11\n\nnotes")).toEqual({
      version: "0.18.0",
      date: "2026-11-11",
    });
  });

  it("skips the Unreleased heading", () => {
    const text = "## [Unreleased]\n\n- wip\n\n## [0.18.0] — 2026-11-11\n\n## [0.17.0] — 2026-11-10\n";
    expect(parseLatestRelease(text)).toEqual({ version: "0.18.0", date: "2026-11-11" });
  });

  it("accepts a hyphen in place of the em dash", () => {
    expect(parseLatestRelease("## [1.2.3] - 2027-01-02")).toEqual({ version: "1.2.3", date: "2027-01-02" });
  });

  it("returns null when there is no release heading", () => {
    expect(parseLatestRelease("# Changelog\n\n## [Unreleased]\n\nnothing\n")).toBeNull();
  });
});

describe("latestRelease", () => {
  it("reads the real changelog", () => {
    const release = latestRelease();
    expect(release?.version).toMatch(/^\d+\.\d+\.\d+$/);
    expect(release?.date).toMatch(/^\d{4}-\d{2}-\d{2}$/);
  });

  it("is null, not a throw, when the changelog cannot be read (the build must not fail on a missing file)", () => {
    const cwd = vi.spyOn(process, "cwd").mockReturnValue("/nonexistent/web");
    try {
      expect(latestRelease()).toBeNull();
    } finally {
      cwd.mockRestore();
    }
  });
});

describe("releaseOf", () => {
  const text = "## [0.18.0] — 2026-11-11\n## [0.17.0] — 2026-10-01";
  it("finds a named version, not just the latest", () => {
    expect(releaseOf(text, "0.17.0")).toEqual({ version: "0.17.0", date: "2026-10-01" });
  });
  it("accepts a hyphen in place of the em dash", () => {
    expect(releaseOf("## [0.17.0] - 2026-10-01", "0.17.0")).toEqual({ version: "0.17.0", date: "2026-10-01" });
  });
  it("is null for a version the changelog does not have", () => {
    expect(releaseOf(text, "9.9.9")).toBeNull();
  });
  it("does not treat the dots of a version as wildcards", () => {
    expect(releaseOf("## [0x17x0] — 2026-10-01", "0.17.0")).toBeNull();
  });
});

describe("filmedRelease", () => {
  it("reads the filmed version from the real changelog", () => {
    const r = filmedRelease(TOUR_PAGE.filmedVersion);
    expect(r.version).toBe(TOUR_PAGE.filmedVersion);
    expect(r.date).toMatch(/^\d{4}-\d{2}-\d{2}$/);
  });
  it("throws for a version the changelog does not have, so a page never states a date it did not read", () => {
    expect(() => filmedRelease("9.9.9")).toThrow(/9\.9\.9/);
  });
});
