import { describe, expect, it } from "vitest";
import en from "./i18n/en.json";
import {
  clipBufferStatus,
  delayStatus,
  destinationStatus,
  ingestPhaseStatus,
  outputStatus,
  processStatus,
  recorderStatus,
  renditionStatus,
} from "./status";

/* One word per state, and the tone decides the colour. The tests pin the
 * operator-facing meaning, not the enum spelling: a failed process must never
 * read as idle (that was the A11 defect: failure looked like disabled), and a
 * stopped destination must never read as green. */

describe("processStatus", () => {
  it("reads running as the only green, live", () => {
    expect(processStatus("running")).toEqual({ labelKey: "state.live", tone: "ok", indicator: "filled" });
  });

  it("keeps failure red and distinct from a stopped (idle) process", () => {
    expect(processStatus("failed").tone).toBe("bad");
    expect(processStatus("failed").indicator).toBe("cross");
    expect(processStatus("stopped").tone).toBe("idle");
    expect(processStatus("failed").indicator).not.toBe(processStatus("stopped").indicator);
  });

  it("shows starting and reconnecting as warn transitions", () => {
    expect(processStatus("starting").tone).toBe("warn");
    expect(processStatus("reconnecting").tone).toBe("warn");
    expect(processStatus("reconnecting").labelKey).toBe("state.reconnecting");
  });

  it("reads an absent process as Offline, the same word stateLabelKey gives", () => {
    expect(processStatus(undefined)).toEqual({ labelKey: "state.offline", tone: "idle", indicator: "ring" });
    expect(processStatus(null).labelKey).toBe("state.offline");
  });
});

describe("destinationStatus", () => {
  it("says Stopped for a disabled destination whatever its process still reports", () => {
    expect(destinationStatus(false, "running")).toEqual({ labelKey: "state.stopped", tone: "idle", indicator: "ring" });
  });

  it("defers to the process state while enabled", () => {
    expect(destinationStatus(true, "running").labelKey).toBe("state.live");
    expect(destinationStatus(true, "failed").tone).toBe("bad");
  });
});

describe("delayStatus, renditionStatus, recorderStatus, clipBufferStatus", () => {
  it("delay: on-air is live, draining is not green", () => {
    expect(delayStatus("on-air").tone).toBe("ok");
    expect(delayStatus("draining")).toMatchObject({ labelKey: "state.draining", tone: "warn" });
    expect(delayStatus("buffering").labelKey).toBe("state.starting");
  });

  it("rendition: ready is idle, not live, so it never looks like air", () => {
    expect(renditionStatus("ready")).toMatchObject({ labelKey: "state.ready", tone: "idle" });
    expect(renditionStatus("transcoding").tone).toBe("warn");
    expect(renditionStatus("attention")).toMatchObject({ labelKey: "state.attention", tone: "warn" });
  });

  it("recorder: switched off is Off, not Offline or Failed", () => {
    expect(recorderStatus(null)).toMatchObject({ labelKey: "state.off", tone: "idle" });
    expect(recorderStatus({ state: "running" } as never).labelKey).toBe("state.live");
  });

  it("clip buffer: on is Ready, off is Off", () => {
    expect(clipBufferStatus(true).labelKey).toBe("state.ready");
    expect(clipBufferStatus(false).labelKey).toBe("state.off");
  });
});

describe("ingestPhaseStatus and outputStatus", () => {
  it("ingest: only receiving is green; unavailable is red, not idle", () => {
    expect(ingestPhaseStatus("receiving").tone).toBe("ok");
    expect(ingestPhaseStatus("unavailable")).toMatchObject({ labelKey: "state.unavailable", tone: "bad" });
    expect(ingestPhaseStatus("unset").labelKey).toBe("state.notSet");
    expect(ingestPhaseStatus("waiting").labelKey).toBe("state.waiting");
    expect(ingestPhaseStatus("off").labelKey).toBe("state.off");
  });

  it("output monitor: stale is a warning, error is red, absent is Offline", () => {
    expect(outputStatus({ kind: "live", report: {} as never }).tone).toBe("ok");
    expect(outputStatus({ kind: "stale", report: {} as never }).tone).toBe("warn");
    expect(outputStatus({ kind: "error", error: "x" }).labelKey).toBe("state.failed");
    expect(outputStatus({ kind: "absent" }).labelKey).toBe("state.offline");
    expect(outputStatus({ kind: "off" }).labelKey).toBe("state.off");
  });
});

describe("every label the vocabulary can return exists in the English catalogue", () => {
  const keys = [
    processStatus("running"), processStatus("starting"), processStatus("reconnecting"),
    processStatus("failed"), processStatus("stopped"), processStatus(undefined),
    destinationStatus(false), delayStatus("buffering"), delayStatus("on-air"), delayStatus("draining"),
    renditionStatus("ready"), renditionStatus("transcoding"), renditionStatus("attention"),
    recorderStatus(null), clipBufferStatus(true), clipBufferStatus(false),
    ingestPhaseStatus("unset"), ingestPhaseStatus("off"), ingestPhaseStatus("unavailable"),
    ingestPhaseStatus("waiting"), ingestPhaseStatus("receiving"),
    outputStatus({ kind: "off" }), outputStatus({ kind: "absent" }), outputStatus({ kind: "starting" }),
    outputStatus({ kind: "error", error: "" }), outputStatus({ kind: "stale", report: {} as never }),
  ];
  it.each([...new Set(keys.map((k) => k.labelKey))])("%s is catalogued", (key) => {
    expect(Object.prototype.hasOwnProperty.call(en, key)).toBe(true);
  });
});
