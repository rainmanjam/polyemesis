/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { loudnessMeasured } from "./meterFacts";

describe("loudnessMeasured", () => {
  it("is false for the placeholder report an analyser publishes before it has measured anything", () => {
    // verdict "unknown" with zero seconds is not a quiet reading, it is no
    // reading: every float is still at its zero value, and zero is the LOUDEST
    // value on both the LUFS and the dBTP scale.
    expect(loudnessMeasured({ verdict: "unknown", seconds: 0 })).toBe(false);
  });

  it("is true once any programme has gone through, even while the verdict is still unknown", () => {
    // The integration window filling is real feedback and the card explains it
    // in prose; blanking it would throw away the only progress an operator sees.
    expect(loudnessMeasured({ verdict: "unknown", seconds: 3 })).toBe(true);
  });

  it("is true for every judged verdict", () => {
    expect(loudnessMeasured({ verdict: "pass", seconds: 30 })).toBe(true);
    expect(loudnessMeasured({ verdict: "warn", seconds: 30 })).toBe(true);
    expect(loudnessMeasured({ verdict: "fail", seconds: 30 })).toBe(true);
  });

  it("claims nothing about a report that is not there", () => {
    expect(loudnessMeasured(null)).toBe(false);
    expect(loudnessMeasured(undefined)).toBe(false);
  });
});

/* AND THAT THE PAGE ACTUALLY ASKS.
 *
 * A decision extracted to lib is only half a fix: a unit test of the function
 * alone passes just as happily with the old expression still inline. These pin
 * the wiring, the way preset-drift.test.ts pins the two copies of the platform
 * catalogue against each other. */
const ROOT = new URL("../../../", import.meta.url).pathname;
const metersPage = () => readFileSync(join(ROOT, "ui/src/pages/MetersPage.tsx"), "utf8");

describe("MetersPage, wired to what it has been told", () => {
  it("seeds the Monitor switch from the server rather than asserting it is on", () => {
    const src = metersPage();
    // The hardcoded seed. It made every remount draw the switch ON over a
    // monitor the operator had switched off.
    expect(src).not.toContain("useState(true)");
    expect(src).toContain("useState<boolean | null>(null)");
    expect(src).toContain("setMonitorOn(v.enabled ?? null)");
    // Indeterminate rather than guessed until the first read answers.
    expect(src).toContain("disabled={monitorOn === null}");
    expect(src).toContain('t("meters.monitorUnknown")');
  });

  it("tracks whether the loudness poll is still succeeding instead of swallowing it", () => {
    const src = metersPage();
    // The empty catch that let the last verdicts sit on screen for ever with no
    // clock and no "as of".
    expect(src).not.toContain(".catch(() => {})");
    expect(src).toContain("useStaleTracker()");
    expect(src).toContain("freshness.failed");
    expect(src).toContain('t("meters.notUpdating")');
  });

  it("routes every compliance stat through loudnessMeasured, not just two of them", () => {
    const src = metersPage();
    expect(src).toContain("const measured = loudnessMeasured(report);");
    // Each of the raw expressions the card used to print unguarded.
    expect(src).toContain("measured ? lufs(report.momentaryLufs)");
    expect(src).toContain("measured ? dbtp(report.truePeakDbtp)");
    expect(src).not.toContain("value={lufs(report.momentaryLufs)}");
    expect(src).not.toContain("value={lufs(report.shortTermLufs)}");
    expect(src).not.toContain("value={lufs(report.rangeLu)}");
    expect(src).not.toContain("value={dbtp(report.truePeakDbtp)}");
    // The red paint on a ceiling nobody measured against.
    expect(src).toContain("measured &&\n    targeted &&");
  });
});
