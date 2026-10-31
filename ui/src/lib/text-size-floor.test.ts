import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import { join, relative } from "node:path";

/* THE 12px FLOOR FOR CONTENT (A94, A50).
 *
 * `text-micro` is 10px, and `text-[9px]` and `text-[10px]` are the same
 * problem written by hand. The council found the warnings an operator most
 * needs (unsaved retention, buffer off, clip notes) set in that size, and the
 * badge that says LIVE set in it too. The floor is 12px (`text-sm`) for
 * anything a person reads as content. The only exceptions are units and axis or
 * scale labels, which DESIGN-SYSTEM.md allows at `text-micro`.
 *
 * Two rules:
 *
 *   1. The shared primitives (components/ui and components/signature) hold the
 *      floor outright, except for the scale labels in AudioMeter and the unit
 *      in Stat, which are listed below with the reason.
 *   2. Everywhere else is a RATCHET. Each file may hold no more of these
 *      utilities than the count recorded here. Converting a usage lowers the
 *      count; adding one fails. A file that is not listed may hold none.
 *
 * Comment lines are not counted, because a comment that explains the old class
 * is not a use of it. */

const SRC = new URL("../", import.meta.url).pathname; // ui/src/

const MICRO = /text-micro\b|text-\[(?:9|10)px\]/g;

function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { recursive: true, withFileTypes: false })
    .map((name) => join(dir, String(name)))
    .filter((p) => /\.(tsx|ts)$/.test(p) && !/\.test\.(tsx|ts)$/.test(p));
}

function microUses(file: string): number {
  let n = 0;
  for (const line of readFileSync(file, "utf8").split("\n")) {
    const t = line.trim();
    if (t.startsWith("*") || t.startsWith("//") || t.startsWith("/*")) continue;
    n += (line.match(MICRO) ?? []).length;
  }
  return n;
}

/** Allowed counts, by file. Lower these as usages convert. Raising one needs a
 *  reason in the PR, because the floor is the thing being defended. */
const ALLOWED: Record<string, number> = {
  "components/AccountLiveStats.tsx": 10,
  "components/AnchorGrid.tsx": 1,
  "components/DestinationCard.tsx": 13,
  "components/FacebookStreamHealth.tsx": 7,
  "components/McpAccessCard.tsx": 3,
  "components/OnAirBar.tsx": 0,
  "components/OverlaySettings.tsx": 11,
  "components/PreviewPlayer.tsx": 3,
  // Scale labels on the meter (dB ticks, peak tags). DESIGN-SYSTEM.md allows
  // the micro step for axis and scale labels.
  "components/signature/AudioMeter.tsx": 3,
  // The unit beside a stat value ("ms", "%"). A unit, not content.
  "components/signature/Stat.tsx": 1,
  "pages/ClipEditor.tsx": 14,
  "pages/ClipsPage.tsx": 4,
  "pages/Dashboard.tsx": 31,
  "pages/LibraryPage.tsx": 6,
  "pages/MonitoringPage.tsx": 20,
  "pages/RecordingsPage.tsx": 8,
};

const PRIMITIVE_DIRS = ["components/ui/", "components/signature/"];

describe("the 12px floor for content", () => {
  const files = sourceFiles(SRC);

  it("the scanner reaches the sources and finds the allowed usages", () => {
    // Positive control: if the walk stops matching, the ratchet below passes
    // vacuously. AudioMeter's scale labels are a known usage.
    expect(files.length).toBeGreaterThan(100);
    expect(microUses(join(SRC, "components/signature/AudioMeter.tsx"))).toBeGreaterThan(0);
  });

  it("the shared primitives hold the floor, bar the listed unit and scale labels", () => {
    const hits: string[] = [];
    for (const f of files) {
      const rel = relative(SRC, f);
      if (!PRIMITIVE_DIRS.some((d) => rel.startsWith(d))) continue;
      const allowed = ALLOWED[rel] ?? 0;
      const n = microUses(f);
      if (n > allowed) hits.push(`${rel} has ${n}, allowed ${allowed}`);
    }
    expect(hits, "shared primitives must use text-sm (12px) for content").toEqual([]);
  });

  it("no file gains a text-micro or 9/10px usage beyond its recorded count", () => {
    const hits: string[] = [];
    for (const f of files) {
      const rel = relative(SRC, f);
      const allowed = ALLOWED[rel] ?? 0;
      const n = microUses(f);
      if (n > allowed) hits.push(`${rel} has ${n}, allowed ${allowed}`);
    }
    expect(hits, "use text-sm (12px) for new text; lower the count when converting").toEqual([]);
  });
});
