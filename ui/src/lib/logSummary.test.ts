import { describe, expect, it } from "vitest";
import type { LogLine } from "@/lib/types";
import {
  collapseRepeats,
  countLevels,
  filterLines,
  verdict,
} from "@/lib/logSummary";

const at = (s: number) => new Date(Date.UTC(2026, 9, 29, 20, 0, s)).toISOString();

const line = (
  s: number,
  process: string,
  text: string,
  level: LogLine["level"] = "error",
): LogLine => ({ time: at(s), process, text, level });

describe("collapseRepeats", () => {
  it("folds consecutive identical lines into one group with a count", () => {
    const groups = collapseRepeats([
      line(1, "preview", "decode_slice_header error"),
      line(2, "preview", "decode_slice_header error"),
      line(3, "preview", "decode_slice_header error"),
    ]);
    expect(groups).toHaveLength(1);
    expect(groups[0].count).toBe(3);
    expect(groups[0].line.text).toBe("decode_slice_header error");
  });

  it("does not merge identical lines that are separated by a different line", () => {
    const groups = collapseRepeats([
      line(1, "preview", "decode_slice_header error"),
      line(2, "preview", "something else", "warning"),
      line(3, "preview", "decode_slice_header error"),
    ]);
    expect(groups.map((g) => g.count)).toEqual([1, 1, 1]);
  });

  it("treats the same text at a different level or process as a different line", () => {
    const groups = collapseRepeats([
      line(1, "preview", "x", "error"),
      line(2, "preview", "x", "warning"),
      line(3, "meters", "x", "warning"),
    ]);
    expect(groups).toHaveLength(3);
  });

  it("returns an empty list for no lines", () => {
    expect(collapseRepeats([])).toEqual([]);
  });
});

describe("countLevels", () => {
  it("counts fatal and error as errors and warning as warnings, ignoring info", () => {
    expect(
      countLevels([
        line(1, "p", "a", "fatal"),
        line(2, "p", "b", "error"),
        line(3, "p", "c", "warning"),
        line(4, "p", "d", "info"),
      ]),
    ).toEqual({ errors: 2, warnings: 1 });
  });
});

describe("filterLines", () => {
  const lines = [
    line(1, "preview", "decode failed", "error"),
    line(2, "preview", "slow write", "warning"),
    line(3, "meters", "level ok", "info"),
  ];

  it("keeps only error-level lines for the errors filter", () => {
    expect(filterLines(lines, "errors", "").map((l) => l.text)).toEqual(["decode failed"]);
  });

  it("keeps only warning-level lines for the warnings filter", () => {
    expect(filterLines(lines, "warnings", "").map((l) => l.text)).toEqual(["slow write"]);
  });

  it("keeps everything for all", () => {
    expect(filterLines(lines, "all", "")).toHaveLength(3);
  });

  it("matches the search case-insensitively against text and process name", () => {
    expect(filterLines(lines, "all", "DECODE").map((l) => l.text)).toEqual(["decode failed"]);
    expect(filterLines(lines, "all", "meters").map((l) => l.text)).toEqual(["level ok"]);
  });
});

describe("verdict", () => {
  const now = Date.parse(at(60));

  it("is clean when the log has no errors at all", () => {
    expect(verdict([line(55, "preview", "slow", "warning")], now)).toEqual({ kind: "clean" });
  });

  it("ignores errors older than the window", () => {
    const v = verdict([line(0, "preview", "old", "error")], now, 30_000);
    expect(v).toEqual({ kind: "clean" });
  });

  it("names the process with the most recent errors", () => {
    const v = verdict(
      [
        line(50, "meters", "a", "error"),
        line(51, "preview", "b", "error"),
        line(52, "preview", "c", "fatal"),
      ],
      now,
    );
    expect(v).toEqual({ kind: "errors", process: "preview", count: 2, windowSec: 60 });
  });
});
