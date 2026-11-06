import { describe, expect, it } from "vitest";
import { monitorVerdict, type MonitorVerdictInput } from "./monitorVerdict";
import { translate } from "@/lib/i18n";

/* The calm verdict reads one as one. The catalogue has no plural rules, so a
 * single running process has its own key: it read "All 1 running, none
 * failed." Translated with an explicit "en", so no language state is touched. */

const calm = (processes: number): MonitorVerdictInput => ({
  processes,
  running: processes,
  failed: 0,
  processesStale: false,
  liveLost: false,
  relayDropped: 0,
  ingestLossPercent: 0,
  cpuPercent: 30,
  cpuWarnPercent: 85,
  memPercent: 40,
});

describe("monitorVerdict: all running reads one as one (en)", () => {
  it("says 1 running, none failed. with one process", () => {
    const v = monitorVerdict(calm(1));
    expect(v.key).toBe("mon.verdictAllRunningOne");
    expect(v.params).toEqual({ count: 1 });
    expect(v.tone).toBe("ok");

    const text = translate("en", v.key, v.params);
    expect(text).toBe("1 running, none failed.");
    expect(text).not.toBe("All 1 running, none failed.");
  });

  it("says All 2 running, none failed. with two processes", () => {
    const v = monitorVerdict(calm(2));
    expect(v.key).toBe("mon.verdictAllRunning");
    expect(v.params).toEqual({ count: 2 });

    expect(translate("en", v.key, v.params)).toBe("All 2 running, none failed.");
  });
});
