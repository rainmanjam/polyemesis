import { describe, expect, it } from "vitest";
import { KEY_STATE, TITLE_MAX, clip, stateFor, stopTitle, titleFor } from "../src/render.js";
import type { KeyFace } from "../src/keyFace.js";

describe("stateFor", () => {
  it("maps every tone to a distinct state index", () => {
    const tones = ["live", "warn", "error", "idle", "held", "unbound"] as const;
    const indexes = tones.map(stateFor);
    expect(new Set(indexes).size).toBe(tones.length);
    expect(indexes.every((i) => i >= 0 && i <= 5)).toBe(true);
  });

  it("gives live its own state, so green is never shared with another tone", () => {
    expect(stateFor("live")).toBe(KEY_STATE.live);
    expect(stateFor("warn")).not.toBe(KEY_STATE.live);
  });
});

describe("clip", () => {
  it("leaves a short title alone", () => {
    expect(clip("Live")).toBe("Live");
  });

  it("cuts a long title and ends it with an ellipsis, within the key width", () => {
    const out = clip("rtmp: server returned 403 Forbidden for the stream key", TITLE_MAX);
    expect([...out].length).toBe(TITLE_MAX);
    expect(out.endsWith("…")).toBe(true);
  });

  it("counts characters, not UTF-16 units, so a surrogate pair is not split", () => {
    const out = clip("🔴".repeat(40), 5);
    expect([...out].length).toBe(5);
  });
});

describe("titleFor", () => {
  it("shows the state label, then the server's reason, in English", () => {
    const face: KeyFace = { tone: "error", label: "streamdeck.state.failed", detail: "stream key rejected" };
    expect(titleFor(face, "en")).toBe("Failed\nstream key rejected");
  });

  it("adds the warning line when a destination carries one", () => {
    const face: KeyFace = { tone: "live", label: "streamdeck.state.live", warning: "streamdeck.warn.backupDown" };
    expect(titleFor(face, "en")).toBe("Live\nBackup down");
  });

  it("renders in the requested locale", () => {
    const face: KeyFace = { tone: "live", label: "streamdeck.state.live" };
    expect(titleFor(face, "de")).toBe("Live");
    expect(titleFor({ tone: "idle", label: "streamdeck.state.off" }, "de")).toBe("Aus");
  });
});

describe("stopTitle", () => {
  it("says what a completed hold would end, counting YouTube broadcasts", () => {
    expect(stopTitle(2, "en")).toBe("Hold to stop\n2 YouTube will end");
  });

  it("says plainly when no YouTube broadcast would end", () => {
    expect(stopTitle(0, "en")).toBe("Hold to stop\nNo YouTube broadcasts");
  });

  it("refuses to imply a count when the count could not be loaded", () => {
    expect(stopTitle(undefined, "en")).toBe("Hold to stop\nCount unavailable");
  });
});
