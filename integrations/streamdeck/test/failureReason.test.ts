import { describe, expect, it } from "vitest";
import { destinationFace } from "../src/keyFace.js";
import { titleFor } from "../src/render.js";
import type { DestStatus } from "../src/model.js";

// #20 on a Stream Deck key face. The server now sends a structured cause, so
// the key can say "Key rejected" on 72 pixels instead of the clipped tail of
// an FFmpeg line, and a held destination shows the held tile.

const dest = (over: Partial<DestStatus> = {}): DestStatus => ({
  id: 1,
  name: "YouTube",
  platform: "youtube",
  enabled: true,
  sourceId: 7,
  process: { state: "failed", lastError: "exit status 251: Error opening output rtmp://a/live2/****: I/O error" },
  ...over,
});

describe("destinationFace names the classified cause", () => {
  it("a failed destination shows the cause, not the raw FFmpeg tail", () => {
    const face = destinationFace(dest({ reason: { cause: "key_rejected" } }), undefined, null);
    expect(face).toMatchObject({ tone: "error", label: "streamdeck.state.failed", reason: "streamdeck.reason.key_rejected" });
    expect(face.detail).toBeUndefined();
    expect(titleFor(face, "en")).toBe("Failed\nKey rejected");
  });

  it("a held destination shows the held tile with its cause", () => {
    const face = destinationFace(dest({ reason: { cause: "key_rejected", held: true } }), undefined, null);
    expect(face).toMatchObject({ tone: "held", label: "streamdeck.state.held", reason: "streamdeck.reason.key_rejected" });
  });

  it("a rotated key on a connected account says so, because that is the fix", () => {
    const face = destinationFace(dest({ reason: { cause: "key_rejected", keyChanged: true } }), undefined, null);
    expect(face.reason).toBe("streamdeck.reason.keyChanged");
  });

  it("an unknown cause keeps the server's own words as the detail", () => {
    const face = destinationFace(
      dest({ reason: { cause: "unknown", line: "Error closing file: Broken pipe" } }),
      undefined,
      null,
    );
    expect(face).toMatchObject({ reason: "streamdeck.reason.unknown", detail: "Error closing file: Broken pipe" });
  });

  it("a reconnecting destination keeps its state and adds the cause", () => {
    const face = destinationFace(dest({ process: { state: "reconnecting" }, reason: { cause: "dns" } }), undefined, null);
    expect(face).toMatchObject({ tone: "warn", label: "streamdeck.state.reconnecting", reason: "streamdeck.reason.dns" });
  });

  it("a running destination with a slow upload is not drawn as plainly live", () => {
    const face = destinationFace(dest({ process: { state: "running" }, reason: { cause: "upload_too_slow" } }), undefined, null);
    expect(face).toMatchObject({ tone: "warn", label: "streamdeck.state.live", reason: "streamdeck.reason.upload_too_slow" });
  });

  it("a cause the plugin does not know yet falls back to the raw line rather than inventing one", () => {
    const face = destinationFace(
      dest({ reason: { cause: "something_new" as never, line: "new words" } }),
      undefined,
      null,
    );
    expect(face.reason).toBeUndefined();
    expect(face.detail).toBe("new words");
  });

  it("without a reason the key behaves exactly as before", () => {
    const face = destinationFace(dest(), undefined, null);
    expect(face).toMatchObject({ tone: "error", label: "streamdeck.state.failed" });
    expect(face.detail).toContain("exit status 251");
  });
});
