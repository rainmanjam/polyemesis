import { describe, expect, it } from "vitest";
import { monitorVerdict, type MonitorVerdictInput } from "./monitorVerdict";

/* The sentence under the Monitoring title. It used to read only the process
 * states, so "All 12 running, none failed." sat directly above a 91% CPU
 * warning: technically true, operationally misleading. The verdict now folds in
 * every tile that can raise a warning on the same page, in the order an
 * operator would want to hear about them. */

const calm: MonitorVerdictInput = {
  processes: 12,
  running: 12,
  failed: 0,
  processesStale: false,
  relayDropped: 0,
  ingestLossPercent: 0,
  cpuPercent: 30,
  cpuWarnPercent: 85,
  memPercent: 40,
};

describe("monitorVerdict", () => {
  it("is calm only when nothing on the page is warning", () => {
    expect(monitorVerdict(calm)).toEqual({
      key: "mon.verdictAllRunning",
      params: { count: 12 },
      tone: "ok",
    });
  });

  it("does not say all running while host CPU is over the saved ceiling", () => {
    const v = monitorVerdict({ ...calm, cpuPercent: 91 });
    expect(v.key).toBe("mon.verdictCpuHigh");
    expect(v.params).toEqual({ pct: 91, limit: 85 });
    expect(v.tone).toBe("warn");
  });

  it("uses the same comparison as the CPU tile: at the ceiling is not over it", () => {
    expect(monitorVerdict({ ...calm, cpuPercent: 85 }).key).toBe("mon.verdictAllRunning");
  });

  it("warns on memory above 90%, the memory tile's own threshold", () => {
    expect(monitorVerdict({ ...calm, memPercent: 91 }).key).toBe("mon.verdictMemoryHigh");
    expect(monitorVerdict({ ...calm, memPercent: 90 }).key).toBe("mon.verdictAllRunning");
  });

  it("says the process list is stale before it says anything the list implies", () => {
    const v = monitorVerdict({ ...calm, processesStale: true });
    expect(v.key).toBe("mon.verdictStale");
    expect(v.tone).toBe("warn");
  });

  it("reports ingest loss from either the drop counter or the loss percentage", () => {
    expect(monitorVerdict({ ...calm, relayDropped: 3 }).key).toBe("mon.verdictIngestLoss");
    expect(monitorVerdict({ ...calm, ingestLossPercent: 0.4 }).key).toBe("mon.verdictIngestLoss");
  });

  it("puts a failure first, ahead of every warning", () => {
    const v = monitorVerdict({
      ...calm,
      failed: 2,
      running: 10,
      processesStale: true,
      relayDropped: 1,
      cpuPercent: 99,
      memPercent: 99,
    });
    expect(v).toEqual({ key: "mon.verdictProcessesFailed", params: { count: 2 }, tone: "bad" });
  });

  it("orders the warnings stale, then loss, then CPU, then memory", () => {
    const all = { ...calm, processesStale: true, relayDropped: 1, cpuPercent: 99, memPercent: 99 };
    expect(monitorVerdict(all).key).toBe("mon.verdictStale");
    expect(monitorVerdict({ ...all, processesStale: false }).key).toBe("mon.verdictIngestLoss");
    expect(monitorVerdict({ ...all, processesStale: false, relayDropped: 0 }).key).toBe("mon.verdictCpuHigh");
    expect(
      monitorVerdict({ ...all, processesStale: false, relayDropped: 0, cpuPercent: 10 }).key,
    ).toBe("mon.verdictMemoryHigh");
  });

  it("counts the not-running processes when nothing else is wrong", () => {
    expect(monitorVerdict({ ...calm, running: 9 })).toEqual({
      key: "mon.verdictSomeNotRunning",
      params: { running: 9, count: 12 },
      tone: "idle",
    });
  });

  it("rounds the CPU figure it prints", () => {
    expect(monitorVerdict({ ...calm, cpuPercent: 91.46 }).params).toEqual({ pct: 91, limit: 85 });
  });

  it("does not warn on host figures it has not read yet", () => {
    expect(
      monitorVerdict({ ...calm, cpuPercent: undefined, memPercent: undefined }).key,
    ).toBe("mon.verdictAllRunning");
  });
});
