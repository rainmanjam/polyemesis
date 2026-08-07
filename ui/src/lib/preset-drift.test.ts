/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

/* The destination preset catalogue exists TWICE.
 *
 * internal/db/platforms.go is the source of truth and is served from
 * GET /api/v1/platforms/presets; ui/src/components/DestinationDialog.tsx keeps
 * a hand-written mirror so the picker works before any fetch resolves. That is
 * a deliberate trade, and it was completely unguarded: adding a platform, or a
 * field, to one side reached the other only if somebody remembered.
 *
 * This was not hypothetical. Researched per-platform encoder guidance was added
 * to the Go catalogue and would never have appeared in the UI — no test, no
 * type error, no warning. The mirror looked correct because it was correct for
 * the fields it already had.
 */

const ROOT = new URL("../../../", import.meta.url).pathname;

function goPresetIDs(): string[] {
  const src = readFileSync(join(ROOT, "internal/db/platforms.go"), "utf8");
  return [...src.matchAll(/ID:\s*"([a-z0-9-]+)",\s*Name:/g)].map((m) => m[1]);
}

function tsPresetIDs(): string[] {
  const src = readFileSync(join(ROOT, "ui/src/components/DestinationDialog.tsx"), "utf8");
  // The TS mirror's entries are `id: "youtube",` inside the PRESETS array.
  const start = src.indexOf("const PRESETS:");
  const end = src.indexOf("\n];", start);
  return [...src.slice(start, end).matchAll(/\bid:\s*"([a-z0-9-]+)"/g)].map((m) => m[1]);
}

describe("the two destination preset catalogues", () => {
  it("contain the same platforms", () => {
    const go = goPresetIDs();
    const ts = tsPresetIDs();
    expect(go.length, "no presets parsed out of platforms.go — the regex has rotted").toBeGreaterThan(20);
    expect(ts.length, "no presets parsed out of DestinationDialog.tsx").toBeGreaterThan(20);

    const missingFromUI = go.filter((id) => !ts.includes(id));
    const missingFromGo = ts.filter((id) => !go.includes(id));

    expect(missingFromUI, `in platforms.go but not in the UI mirror: ${missingFromUI.join(", ")}`).toEqual([]);
    expect(missingFromGo, `in the UI mirror but not in platforms.go: ${missingFromGo.join(", ")}`).toEqual([]);
  });

  it("agree on which platforms carry encoder guidance", () => {
    // Guidance is researched, sourced and dated in Go. The UI does not need to
    // duplicate the numbers, but it must not silently disagree about WHICH
    // platforms have them — that is what decides whether the operator is
    // offered a starting point at all.
    const src = readFileSync(join(ROOT, "internal/db/platforms.go"), "utf8");
    const withGuidance = [...src.matchAll(/ID:\s*"([a-z0-9-]+)",\s*Name:[^\n]*\n\s*Video:/g)].map((m) => m[1]);
    expect(withGuidance.length, "platforms.go has no VideoGuidance at all").toBeGreaterThan(5);

    // Every platform carrying guidance must exist in the UI mirror, or the
    // guidance can never be shown.
    const ts = tsPresetIDs();
    const orphaned = withGuidance.filter((id) => !ts.includes(id));
    expect(orphaned, `have guidance in Go but no UI preset to show it on: ${orphaned.join(", ")}`).toEqual([]);
  });
});
