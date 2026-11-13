import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

type Legacy = Record<string, { h1: string; h2: string[]; text: string }>;

const fixture = JSON.parse(
  readFileSync(new URL("./legacy-inner.fixture.json", import.meta.url), "utf8"),
) as Legacy;

const ROUTES = [
  "/comparison",
  "/vs/aitum-multistream",
  "/vs/obs-multi-rtmp",
  "/vs/restreamer",
  "/vs/streamelements",
  "/vs/streamlabs",
  "/multistream",
  "/free-restream-service",
  "/how-to-multistream-from-obs",
  "/twitch-vod-track",
  "/calculator",
  "/docs",
];

describe("legacy inner-page fixture", () => {
  it("has exactly the 12 frozen routes", () => {
    expect(Object.keys(fixture).sort((a, b) => a.localeCompare(b))).toEqual(
      [...ROUTES].sort((a, b) => a.localeCompare(b)),
    );
  });

  it.each(ROUTES)("%s has a heading and real copy", (route) => {
    expect(fixture[route].h1.length).toBeGreaterThan(0);
    expect(fixture[route].text.length).toBeGreaterThanOrEqual(1500);
  });

  it("keeps the vs/restreamer facts", () => {
    expect(fixture["/vs/restreamer"].text).toContain("15 august 2026");
    expect(fixture["/vs/restreamer"].text).toContain("$19/mo");
  });

  it("is the old comparison copy, without the retired phrase", () => {
    expect(fixture["/comparison"].text).toContain("years of production deployment");
    for (const entry of Object.values(fixture)) {
      expect(entry.text).not.toMatch(/one[- ]maintainer/i);
    }
  });
});
