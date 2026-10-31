import { describe, expect, it } from "vitest";
import { destinationFace } from "../src/keyFace.js";
import { titleFor } from "../src/render.js";
import type { DestStatus } from "../src/model.js";

// #11 on the Stream Deck: a delayed key carries a marker, a key filling its
// delay is not "Off", and a STOPPED key still airing its buffered tail is not
// "Off" either -- that one is the reading that would leave an operator
// believing something was off the air while it was not.
const base = (over: Partial<DestStatus> = {}): DestStatus => ({
  id: 1,
  name: "YouTube",
  platform: "youtube",
  enabled: true,
  sourceId: 7,
  process: { state: "running" },
  delaySeconds: 30,
  ...over,
});

describe("destinationFace: the broadcast delay", () => {
  it("marks a delayed destination that is live", () => {
    const face = destinationFace(base({ delay: { seconds: 30, phase: "on-air" } }), undefined, null);
    expect(face).toMatchObject({ tone: "live", label: "streamdeck.state.live", delaySeconds: 30 });
    expect(titleFor(face, "en")).toContain("Delay 30s");
  });

  it("shows buffering, not off, while the delay fills and the process still reads stopped", () => {
    const face = destinationFace(
      base({ process: { state: "stopped" }, delay: { seconds: 30, phase: "buffering" } }),
      undefined,
      null,
    );
    expect(face).toMatchObject({ tone: "warn", label: "streamdeck.state.buffering" });
  });

  it("shows the tail still airing, not off, on a stopped destination that is draining", () => {
    const face = destinationFace(
      base({ enabled: false, delay: { seconds: 30, phase: "draining", drainRemainingSeconds: 9 } }),
      undefined,
      null,
    );
    expect(face).toMatchObject({ tone: "warn", label: "streamdeck.state.draining" });
  });

  // The delay marker and the issue list meet on one face: a delayed key with an
  // open issue still says it is delayed, beneath the issue and its "+N".
  it("keeps the marker on a delayed key whose face is an issue, with the +N badge", () => {
    const face = destinationFace(
      base({ delay: { seconds: 30, phase: "on-air" }, accountHealth: "expired", restartStorm: 8 }),
      undefined,
      null,
    );
    expect(face).toMatchObject({ tone: "error", reason: "streamdeck.issue.reconnectAccount", more: 1, delaySeconds: 30 });
    expect(titleFor(face, "en").split("\n").at(-1)).toBe("Delay 30s");
  });

  it("adds no marker to a destination with no delay", () => {
    const face = destinationFace(base({ delaySeconds: undefined }), undefined, null);
    expect(face.delaySeconds).toBeUndefined();
    expect(titleFor(face, "en")).not.toContain("Delay");
  });
});
