import { describe, expect, it } from "vitest";

import { clock, isoDuration, videoLd } from "./video-ld";

describe("isoDuration", () => {
  it.each([
    [90, "PT1M30S"],
    [60, "PT1M"],
    [45, "PT45S"],
    [3600, "PT1H"],
    [3725, "PT1H2M5S"],
    [0, "PT0S"],
  ])("%s seconds is %s", (seconds, expected) => expect(isoDuration(seconds)).toBe(expected));

  it("rounds a fraction of a second down", () => expect(isoDuration(98.2)).toBe("PT1M38S"));
});

describe("clock", () => {
  it.each([
    [0, "0:00"],
    [65, "1:05"],
    [600, "10:00"],
    [22.2, "0:22"],
  ])("%s seconds reads %s", (seconds, expected) => expect(clock(seconds)).toBe(expected));
});

describe("videoLd", () => {
  const tour = {
    src: "/media/t.mp4",
    poster: "/media/t.jpg",
    width: 1920,
    height: 1080,
    duration: 90,
    chapters: [
      { title: "A", start: 0 },
      { title: "B", start: 30 },
      { title: "C", start: 60 },
    ],
  };
  const ld = videoLd({
    site: "https://example.test",
    route: "/tour",
    name: "Tour",
    description: "Desc",
    tour,
    uploadDate: "2026-11-11",
  }) as Record<string, any>;

  it("is a VideoObject with the page's own fields", () => {
    expect(ld["@context"]).toBe("https://schema.org");
    expect(ld["@type"]).toBe("VideoObject");
    expect(ld.name).toBe("Tour");
    expect(ld.description).toBe("Desc");
    expect(ld.uploadDate).toBe("2026-11-11");
    expect(ld.duration).toBe("PT1M30S");
    expect(ld.thumbnailUrl).toEqual(["https://example.test/media/t.jpg"]);
    expect(ld.contentUrl).toBe("https://example.test/media/t.mp4");
  });

  it("has one clip per chapter, each ending where the next begins", () => {
    expect(ld.hasPart).toHaveLength(3);
    expect(ld.hasPart[1]).toEqual({
      "@type": "Clip",
      name: "B",
      startOffset: 30,
      endOffset: 60,
      url: "https://example.test/tour#chapter-2",
    });
    expect(ld.hasPart[2].endOffset).toBe(90);
    expect(ld.hasPart[0].url.endsWith("#chapter-1")).toBe(true);
  });

  it("carries nothing the page does not show", () => {
    for (const key of ["embedUrl", "interactionStatistic", "aggregateRating"]) expect(ld).not.toHaveProperty(key);
  });
});
