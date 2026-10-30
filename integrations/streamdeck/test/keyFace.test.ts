import { describe, expect, it } from "vitest";
import { destinationFace, failoverFace } from "../src/keyFace.js";
import type { DestStatus, HoldStatus } from "../src/model.js";

const base = (over: Partial<DestStatus> = {}): DestStatus => ({
  id: 1,
  name: "Twitch",
  platform: "twitch",
  enabled: true,
  sourceId: 7,
  process: { state: "running" },
  ...over,
});

describe("destinationFace: the face never lies", () => {
  it("shows live only for a running, unstalled process", () => {
    expect(destinationFace(base(), undefined, null)).toMatchObject({ tone: "live", label: "streamdeck.state.live" });
  });

  it("shows stalled, not live, when the process is running but its output has stopped", () => {
    expect(destinationFace(base({ stalled: true }), undefined, null)).toMatchObject({
      tone: "warn",
      label: "streamdeck.state.stalled",
    });
  });

  it("shows starting, not off, for an enabled destination whose process is absent (#462)", () => {
    expect(destinationFace(base({ process: null }), undefined, null)).toMatchObject({
      tone: "warn",
      label: "streamdeck.state.starting",
    });
  });

  it("shows starting for a destination mid-reconcile even when its process reads stopped", () => {
    expect(destinationFace(base({ process: { state: "stopped" }, transitioning: true }), undefined, null)).toMatchObject({
      tone: "warn",
      label: "streamdeck.state.starting",
    });
  });

  it("shows off, not live, for a disabled destination with no process", () => {
    expect(destinationFace(base({ enabled: false, process: null }), undefined, null)).toMatchObject({
      tone: "idle",
      label: "streamdeck.state.off",
    });
  });

  it("shows the server's own reason on a failed process", () => {
    const face = destinationFace(
      base({ process: { state: "failed", lastError: "rtmp: server returned 403 Forbidden" } }),
      undefined,
      null,
    );
    expect(face).toMatchObject({
      tone: "error",
      label: "streamdeck.state.failed",
      detail: "rtmp: server returned 403 Forbidden",
    });
  });

  it("falls back to the destination's error when the process carries no reason", () => {
    const face = destinationFace(base({ process: { state: "failed" }, error: "stream key rejected" }), undefined, null);
    expect(face.detail).toBe("stream key rejected");
  });

  it("shows reconnecting in the warn tone and passes the retry reason through", () => {
    expect(
      destinationFace(base({ process: { state: "reconnecting", lastError: "connection reset" } }), undefined, null),
    ).toMatchObject({ tone: "warn", label: "streamdeck.state.reconnecting", detail: "connection reset" });
  });

  it("carries the server's stop warning as a badge on the key", () => {
    const face = destinationFace(base({ stopWarning: "Stopping ends the YouTube broadcast" }), undefined, null);
    expect(face).toMatchObject({
      tone: "live",
      warning: "streamdeck.warn.stopWarning",
      warningDetail: "Stopping ends the YouTube broadcast",
    });
  });

  it("surfaces a dead backup beside a healthy primary instead of hiding it", () => {
    const face = destinationFace(
      base({ backupProcess: { state: "failed", lastError: "backup source gone" } }),
      undefined,
      null,
    );
    expect(face.tone).toBe("live");
    expect(face.warning).toBe("streamdeck.warn.backupDown");
    expect(face.warningDetail).toBe("backup source gone");
  });

  it("surfaces a backup the server says could not be started", () => {
    const face = destinationFace(base({ backupError: "no slate configured" }), undefined, null);
    expect(face.warning).toBe("streamdeck.warn.backupDown");
    expect(face.warningDetail).toBe("no slate configured");
  });

  it("never shows a destination that belongs to another programme", () => {
    const face = destinationFace(base({ sourceId: 9 }), 7, null);
    expect(face).toEqual({ tone: "unbound", label: "streamdeck.state.otherProgramme" });
    expect(face).not.toHaveProperty("detail");
  });

  it("shows a destination on the bound programme", () => {
    expect(destinationFace(base({ sourceId: 7 }), 7, null).tone).toBe("live");
  });

  it("shows any programme when the key is not bound to one (single-programme install)", () => {
    expect(destinationFace(base({ sourceId: 9 }), undefined, null).tone).toBe("live");
  });

  it("reports a destination the server no longer has", () => {
    expect(destinationFace(undefined, undefined, null)).toEqual({ tone: "unbound", label: "streamdeck.state.missing" });
  });

  it("shows held, with the server's reason, when the engine deliberately runs nothing", () => {
    const hold: HoldStatus = { code: "no_source", reason: "No programme is on air." };
    expect(destinationFace(base({ process: null }), undefined, hold)).toMatchObject({
      tone: "held",
      label: "streamdeck.state.held",
      detail: "No programme is on air.",
    });
  });
});

describe("failoverFace", () => {
  it("names primary as on air in the live tone", () => {
    expect(failoverFace({ active: "primary" })).toMatchObject({ tone: "live", label: "streamdeck.failover.primary" });
  });

  it("names the backup and slate as on air in the warn tone, with the selector's reason", () => {
    expect(failoverFace({ active: "backup", reason: "primary dropped" })).toMatchObject({
      tone: "warn",
      label: "streamdeck.failover.backup",
      detail: "primary dropped",
    });
    expect(failoverFace({ active: "slate" })).toMatchObject({ tone: "warn", label: "streamdeck.failover.slate" });
  });

  it("says there is no feed info when the server has not answered", () => {
    expect(failoverFace(null)).toEqual({ tone: "idle", label: "streamdeck.failover.unknown" });
  });

  it("shows the selector's error in the error tone", () => {
    expect(failoverFace({ active: "slate", error: "backup failed to start" })).toMatchObject({
      tone: "error",
      detail: "backup failed to start",
    });
  });
});
