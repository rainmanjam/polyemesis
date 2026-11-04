import { describe, expect, it } from "vitest";

import { ingestChipReading, ingestProblem, ingestReadingKnown } from "./ingestChip";
import type { ProcessStatus } from "./types";

const proc = (over: Partial<ProcessStatus>): ProcessStatus =>
  ({ name: "ingest", kind: "ingest", state: "failed", pid: 0, restarts: 0, uptimeSec: 0, ...over }) as ProcessStatus;

describe("ingestProblem (B1: the ingest's non-pass reason)", () => {
  it("returns the server's reason when the ingest is down with one", () => {
    expect(ingestProblem(proc({ lastError: "bind: address in use" }), false, false)).toBe("bind: address in use");
  });

  it("says nothing while bytes are arriving, whatever the process says", () => {
    expect(ingestProblem(proc({ lastError: "old error" }), true, false)).toBeNull();
  });

  it("says nothing while the backup is on air", () => {
    expect(ingestProblem(proc({ lastError: "primary lost" }), false, true)).toBeNull();
  });

  it("says nothing for a running process or one with no reason", () => {
    expect(ingestProblem(proc({ state: "running", lastError: "stale" }), false, false)).toBeNull();
    expect(ingestProblem(proc({ lastError: "" }), false, false)).toBeNull();
    expect(ingestProblem(null, false, false)).toBeNull();
  });
});

describe("ingestChipReading before the first reading", () => {
  it("is pending, not a stopped state, when nothing has been read", () => {
    expect(ingestChipReading(null, false, false, false)).toEqual({ kind: "pending" });
  });

  it("shows a process state the server reported, known or not", () => {
    expect(ingestChipReading(proc({ state: "failed" }), false, false, false)).toEqual({ kind: "state", state: "failed" });
  });

  it("reads running when bytes are arriving, known or not", () => {
    expect(ingestChipReading(null, true, false, false)).toEqual({ kind: "state", state: "running" });
  });

  it("is Stopped once the inputs are known and nothing is arriving", () => {
    expect(ingestChipReading(null, false, false, true)).toEqual({ kind: "state", state: undefined });
  });
});

describe("ingestReadingKnown", () => {
  const snap = (probed: boolean, rxBytes?: number) => ({
    source: { probed },
    relay: rxBytes === undefined ? null : { rxBytes },
  });
  const base = { sourceCount: 3, programmeKbps: null, seriesSamples: 0, seriesProbed: true };

  it("is unknown before the programme's first frame", () => {
    expect(ingestReadingKnown({ ...base, snapshot: null })).toBe(false);
  });

  it("waits for a relay rate on a multi-programme install", () => {
    expect(ingestReadingKnown({ ...base, snapshot: snap(true, 100) })).toBe(false);
    expect(ingestReadingKnown({ ...base, snapshot: snap(true, 100), programmeKbps: 0 })).toBe(true);
  });

  it("knows at once when nothing is probed or there is no counter to rate", () => {
    expect(ingestReadingKnown({ ...base, snapshot: snap(false, 100) })).toBe(true);
    expect(ingestReadingKnown({ ...base, snapshot: snap(true) })).toBe(true);
  });

  it("waits for the bitrate series on a single-programme install", () => {
    const one = { ...base, sourceCount: 1, snapshot: snap(true, 100) };
    expect(ingestReadingKnown(one)).toBe(false);
    expect(ingestReadingKnown({ ...one, seriesSamples: 1 })).toBe(true);
    expect(ingestReadingKnown({ ...one, seriesProbed: false })).toBe(true);
  });
});
