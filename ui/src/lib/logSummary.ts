import type { LogLine } from "@/lib/types";

/** The three views the log panel offers. Errors is the default: a healthy
 *  stream writes little, so the lines an operator needs are the failures. */
export type LogLevelFilter = "errors" | "warnings" | "all";

export interface LogGroup {
  /** The first line of a run of identical lines. */
  line: LogLine;
  /** How many consecutive identical lines it stands for. */
  count: number;
}

export interface LevelCounts {
  errors: number;
  warnings: number;
}

export type Verdict =
  | { kind: "clean" }
  | { kind: "errors"; process: string; count: number; windowSec: number };

/** Error and fatal are both failures, so they share one count. */
export const isErrorLine = (l: LogLine) => l.level === "error" || l.level === "fatal";
const isWarning = (l: LogLine) => l.level === "warning";

/** The window the verdict looks back over. Shown in the verdict text. */
export const VERDICT_WINDOW_SEC = 60;

const sameLine = (a: LogLine, b: LogLine) =>
  a.process === b.process && a.level === b.level && a.text === b.text;

/** Folds each run of identical consecutive lines into one group. FFmpeg repeats
 *  a decode error verbatim for as long as the fault lasts; showing the count
 *  reads as "this happened 214 times" instead of a wall of red. Only adjacent
 *  lines fold, so a repeat separated by something different stays visible. */
export function collapseRepeats(lines: LogLine[]): LogGroup[] {
  const groups: LogGroup[] = [];
  for (const l of lines) {
    const last = groups[groups.length - 1];
    if (last && sameLine(last.line, l)) {
      last.count += 1;
    } else {
      groups.push({ line: l, count: 1 });
    }
  }
  return groups;
}

export function countLevels(lines: LogLine[]): LevelCounts {
  let errors = 0;
  let warnings = 0;
  for (const l of lines) {
    if (isErrorLine(l)) errors += 1;
    else if (isWarning(l)) warnings += 1;
  }
  return { errors, warnings };
}

/** Applies the level view and a free-text search. The search is a
 *  case-insensitive substring match over the message and the process name. */
export function filterLines(
  lines: LogLine[],
  level: LogLevelFilter,
  search: string,
): LogLine[] {
  const needle = search.trim().toLowerCase();
  return lines.filter((l) => {
    if (level === "errors" && !isErrorLine(l)) return false;
    if (level === "warnings" && !isWarning(l)) return false;
    if (needle === "") return true;
    return (
      l.text.toLowerCase().includes(needle) ||
      l.process.toLowerCase().includes(needle)
    );
  });
}

/** The one-line status above the log. It names the process with the most
 *  errors in the trailing window, or says nothing is wrong. It does not claim
 *  the output is unaffected: the log cannot prove that. */
export function verdict(
  lines: LogLine[],
  nowMs: number,
  windowMs = VERDICT_WINDOW_SEC * 1000,
): Verdict {
  const perProcess = new Map<string, number>();
  for (const l of lines) {
    if (!isErrorLine(l)) continue;
    const at = Date.parse(l.time);
    if (Number.isNaN(at) || at > nowMs || nowMs - at > windowMs) continue;
    perProcess.set(l.process, (perProcess.get(l.process) ?? 0) + 1);
  }
  let top: { process: string; count: number } | null = null;
  for (const [process, count] of perProcess) {
    if (!top || count > top.count) top = { process, count };
  }
  if (!top) return { kind: "clean" };
  return { kind: "errors", process: top.process, count: top.count, windowSec: Math.round(windowMs / 1000) };
}
