import { describe, expect, it } from "vitest";

import { ingestProblem } from "./ingestChip";
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
