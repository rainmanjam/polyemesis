import { describe, expect, it } from "vitest";

import { trackChipTitle, trackLabels } from "@/lib/trackLabels";
import type { SourceInfo, SourceTrack } from "@/lib/types";

const track = (index: number, title?: string): SourceTrack => ({
  index,
  channels: 1,
  codec: "aac",
  layout: "mono",
  title,
});

const source = (over: Partial<SourceInfo> = {}): SourceInfo => ({
  id: 1,
  name: "Studio A",
  probed: true,
  tracks: [track(0), track(1), track(2)],
  ...over,
});

describe("trackLabels", () => {
  it("prefers the operator's annotation over the container's title", () => {
    const got = trackLabels(
      source({
        tracks: [track(0, "Embedded mic"), track(1)],
        annotations: [{ track: 0, label: "Host mic" }, { track: 1, label: "Music bed" }],
      }),
      1,
    );
    expect(got).toEqual(["Host mic", "Music bed"]);
  });

  it("keeps the container title when an annotation exists but its label is blank", () => {
    // An operator who clears the label field is not asking for the track to
    // become anonymous -- they are removing THEIR name for it, which should
    // reveal the one the container already carried.
    const got = trackLabels(
      source({
        tracks: [track(0, "Embedded mic")],
        annotations: [{ track: 0, label: "   " }],
      }),
      1,
    );
    expect(got).toEqual(["Embedded mic"]);
  });

  it("leaves unnamed tracks undefined rather than inventing a placeholder", () => {
    const got = trackLabels(
      source({ tracks: [track(0, "Host mic"), track(1), track(2, "Commentary")] }),
      1,
    );
    expect(got).toEqual(["Host mic", undefined, "Commentary"]);
  });

  /* THE ONE THAT MATTERS. On a multi-programme install the dashboard draws a
   * card for every programme's destinations while `source` describes only the
   * selected one. Naming another programme's tracks from it would be a
   * confident wrong answer, which is worse than the unnamed tooltip it
   * replaced -- nothing on screen would tell the reader it is about the wrong
   * show. */
  it("refuses to name tracks when the snapshot is of a different programme", () => {
    const studioA = source({
      id: 1,
      annotations: [{ track: 0, label: "Host mic" }],
    });
    expect(trackLabels(studioA, 2)).toBeNull();
    expect(trackLabels(studioA, null)).toBeNull();
    expect(trackLabels(studioA, undefined)).toBeNull();
  });

  it("returns null when nothing has a name, so callers keep the plain wording", () => {
    expect(trackLabels(source(), 1)).toBeNull();
    expect(trackLabels(source({ tracks: null, annotations: null }), 1)).toBeNull();
    expect(trackLabels(null, 1)).toBeNull();
  });
});

describe("trackChipTitle", () => {
  it("reads as a sentence with and without a name", () => {
    expect(trackChipTitle(2, true, "Commentary")).toBe("Track 3 (Commentary) is included");
    expect(trackChipTitle(2, false, "Commentary")).toBe("Track 3 (Commentary) is excluded");
    expect(trackChipTitle(0, true)).toBe("Track 1 is included");
    expect(trackChipTitle(5, false)).toBe("Track 6 is excluded");
  });
});
