import { describe, expect, it } from "vitest";
import { destinationFace } from "../src/keyFace.js";
import { titleFor } from "../src/render.js";
import type { DestStatus } from "../src/model.js";

// #7 on a Stream Deck key. A YouTube pair is one destination, so its key covers BOTH
// halves: the horizontal key's state stays the truth about the horizontal half, and a
// failing or stopped vertical half is added as an issue on that same key, so the
// operator never sees a green LIVE while the 9:16 copy has gone dark.

const horizontal = (over: Partial<DestStatus> = {}): DestStatus => ({
  id: 1,
  name: "YouTube main",
  platform: "youtube",
  enabled: true,
  sourceId: 7,
  process: { state: "running" },
  youtubeVertical: { verticalId: 2, pairingConfirmed: true },
  ...over,
});

const vertical = (over: Partial<DestStatus> = {}): DestStatus => ({
  id: 2,
  name: "YouTube main (vertical)",
  platform: "youtube",
  enabled: true,
  sourceId: 7,
  verticalHalfOf: 1,
  process: { state: "running" },
  ...over,
});

describe("a YouTube pair's key covers both halves", () => {
  it("is quiet when both halves are running", () => {
    const face = destinationFace(horizontal(), undefined, null, undefined, vertical());
    expect(face.issues ?? []).toEqual([]);
    expect(face.tone).toBe("live");
  });

  it("names a failed vertical half on the key and keeps the horizontal state", () => {
    const face = destinationFace(
      horizontal(),
      undefined,
      null,
      undefined,
      vertical({ error: "the vertical stream was rejected" }),
    );
    expect(face.tone).toBe("error");
    expect(face.reason).toBe("streamdeck.issue.verticalHalfFailed");
    expect(face.label).toBe("streamdeck.state.live");
    expect(titleFor(face, "en")).toContain("Vertical half failed");
  });

  it("warns when the vertical half is stopped while its horizontal half is on air", () => {
    const face = destinationFace(horizontal(), undefined, null, undefined, vertical({ process: { state: "stopped" } }));
    expect(face.tone).toBe("warn");
    expect(face.reason).toBe("streamdeck.issue.verticalHalfDown");
  });

  it("does not warn about a vertical half when the horizontal half is itself stopped", () => {
    const face = destinationFace(
      horizontal({ process: { state: "stopped" } }),
      undefined,
      null,
      undefined,
      vertical({ process: { state: "stopped" } }),
    );
    expect(face.reason).not.toBe("streamdeck.issue.verticalHalfDown");
  });

  it("ignores a vertical row that is not this destination's half", () => {
    const face = destinationFace(
      horizontal({ youtubeVertical: undefined }),
      undefined,
      null,
      undefined,
      vertical({ error: "unrelated" }),
    );
    expect(face.reason).toBeUndefined();
  });
});
