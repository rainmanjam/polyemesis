import { describe, expect, it, vi } from "vitest";

import { latestRelease, parseLatestRelease } from "./release";

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
