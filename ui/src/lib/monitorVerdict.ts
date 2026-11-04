import type { TranslationKey } from "./i18n";

/* THE ONE SENTENCE UNDER THE MONITORING TITLE (A66).
 *
 * It says whether the stream is healthy before any tile is read. It used to
 * read only the child-process states, so "All 12 running, none failed." could
 * sit directly above a 91% CPU warning: true about the processes, wrong about
 * the stream. It now folds in every tile on the same page that can warn, using
 * the same thresholds those tiles use, so the sentence and the tiles cannot
 * disagree.
 *
 * Order is what an operator needs to hear first:
 *   1. a failed process (bad)
 *   1b. the live socket has dropped since it last connected, so every host
 *      and relay figure on the page is frozen (warn)
 *   2. a process list that has stopped refreshing, because every reading
 *      below it is then out of date (warn)
 *   3. packets lost or dropped on the relay (warn)
 *   4. host CPU above the operator's saved Jobs ceiling (warn)
 *   5. host memory above 90% (warn)
 *   6. some processes not running (idle)
 *   7. all running (ok)
 *
 * Presentation only: it reads state and returns a translation key. */

export interface MonitorVerdictInput {
  processes: number;
  running: number;
  failed: number;
  /** The process poll has failed long enough to be called stale. */
  processesStale: boolean;
  /** The live socket was connected and is not now: CPU, memory and relay
   *  figures are the last ones it sent. */
  liveLost: boolean;
  relayDropped: number;
  ingestLossPercent: number;
  /** Undefined until the first host sample arrives. */
  cpuPercent?: number;
  /** The Jobs CPU ceiling the operator saved; the CPU tile warns above it. */
  cpuWarnPercent: number;
  memPercent?: number;
}

export type MonitorVerdictTone = "ok" | "warn" | "bad" | "idle";

export interface MonitorVerdict {
  key: TranslationKey;
  params: Record<string, number>;
  tone: MonitorVerdictTone;
}

/** The memory tile's threshold. One constant, read by the tile and the verdict. */
export const MEMORY_WARN_PERCENT = 90;

export function cpuOver(cpuPercent: number | undefined, ceiling: number): boolean {
  return (cpuPercent ?? 0) > ceiling;
}

export function memoryOver(memPercent: number | undefined): boolean {
  return (memPercent ?? 0) > MEMORY_WARN_PERCENT;
}

export function monitorVerdict(v: MonitorVerdictInput): MonitorVerdict {
  if (v.failed > 0) {
    return { key: "mon.verdictProcessesFailed", params: { count: v.failed }, tone: "bad" };
  }
  if (v.liveLost) {
    return { key: "chrome.socketOfflineHint", params: {}, tone: "warn" };
  }
  if (v.processesStale) {
    return { key: "mon.verdictStale", params: {}, tone: "warn" };
  }
  if (v.relayDropped > 0 || v.ingestLossPercent > 0) {
    return { key: "mon.verdictIngestLoss", params: {}, tone: "warn" };
  }
  if (cpuOver(v.cpuPercent, v.cpuWarnPercent)) {
    return {
      key: "mon.verdictCpuHigh",
      params: { pct: Math.round(v.cpuPercent ?? 0), limit: v.cpuWarnPercent },
      tone: "warn",
    };
  }
  if (memoryOver(v.memPercent)) {
    return {
      key: "mon.verdictMemoryHigh",
      params: { pct: Math.round(v.memPercent ?? 0), limit: MEMORY_WARN_PERCENT },
      tone: "warn",
    };
  }
  if (v.running !== v.processes) {
    return {
      key: "mon.verdictSomeNotRunning",
      params: { running: v.running, count: v.processes },
      tone: "idle",
    };
  }
  return {
    key: v.processes === 1 ? "mon.verdictAllRunningOne" : "mon.verdictAllRunning",
    params: { count: v.processes },
    tone: "ok",
  };
}
