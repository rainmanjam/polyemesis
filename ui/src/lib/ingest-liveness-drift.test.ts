/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

/* "Is the ingest live?" has ONE correct answer in this app, and asking it the
 * obvious way gets the wrong one.
 *
 * engine.reconcileIngest returns early for SRT on purpose -- srtserver delivers
 * datagrams straight into the hub, and a second process on that socket would
 * crash-loop -- so `status.ingest` is null on a HEALTHY SRT install and
 * stateLabel(undefined) renders "Offline". The answer is not imprecise, it is
 * inverted: the healthier the install, the more confidently it lies.
 *
 * This was found, understood and fixed in AppLayout.tsx, whose comment records
 * that a committed screenshot showed "Offline" beside three live destinations.
 * The fix reached the header and not Dashboard.tsx, and the second call site
 * went on saying the ingest was down over three live programmes until a demo
 * capture photographed it (#514).
 *
 * Nothing made useIngestLive the only reachable answer, so this guards the
 * class rather than the instance: a THIRD call site would otherwise appear the
 * same way the second one did. Any file that renders a state label from
 * ingest?.state must also CALL useIngestLive.
 *
 * The check is for "useIngestLive(", the call, not the bare name. Both files
 * here explain this hazard in a comment that names the hook, so matching the
 * name alone let a mutation that deleted the call go green -- observed while
 * writing this test.
 */

const ROOT = new URL("../../../", import.meta.url).pathname;

function sourceFiles(dir: string, exts: readonly string[]): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...sourceFiles(p, exts));
    else if (exts.some((e) => entry.name.endsWith(e)) && !entry.name.includes(".test.")) out.push(p);
  }
  return out;
}

const tsxFiles = (dir: string) => sourceFiles(dir, [".tsx"]);

/** Comments explain the hazard and name the very expression this bans, so they
 *  are not code. Crude on purpose: it only has to be right for this repo. */
function withoutComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:])\/\/.*$/gm, "$1");
}

describe("ingest liveness", () => {
  it("is never labelled from the process state alone", () => {
    const offenders: string[] = [];
    for (const file of tsxFiles(join(ROOT, "ui/src"))) {
      const src = readFileSync(file, "utf8");
      if (!src.includes("stateLabel(ingest?.state)") && !src.includes("ingestChipReading(")) continue;
      // useProgrammeIngest is useIngestLive narrowed to the selected
      // programme, and calls it; the last test below pins that call.
      if (!src.includes("useIngestLive(") && !src.includes("useProgrammeIngest(")) {
        offenders.push(file.slice(ROOT.length));
      }
    }
    expect(
      offenders,
      "these files label the ingest from status.ingest alone, which reads " +
        "Offline on a healthy SRT install -- use useIngestLive (see #514)",
    ).toEqual([]);
  });

  it("is still decided by useIngestLive when a page reads it through useProgrammeIngest", () => {
    const hook = readFileSync(join(ROOT, "ui/src/hooks/useProgrammeIngest.ts"), "utf8");
    expect(hook).toContain("useIngestLive(");
  });

  it("still finds the call sites it is meant to guard", () => {
    // Without this the rule above passes trivially the day someone renames
    // stateLabel or the ingest field, and the guard silently stops guarding.
    // ingestChipReading( is the shape both call sites take now: the header
    // chip and the Ingest card label the ingest through it, with the liveness
    // and known-ness from useProgrammeIngest.
    const guarded = tsxFiles(join(ROOT, "ui/src")).filter((f) => {
      const src = readFileSync(f, "utf8");
      return src.includes("stateLabel(ingest?.state)") || src.includes("ingestChipReading(");
    });
    expect(guarded.length).toBeGreaterThan(0);
  });

  // "Is it live" is lib/liveness.ts's to answer, for destinations AND the
  // ingest, and in .ts helpers as well as .tsx views: this guard used to read
  // only .tsx, so headerLive, attention, ingestChip and dashboardFacts each
  // kept their own copy of the raw reading.
  describe("one liveness module", () => {
    const MODULE = "ui/src/lib/liveness.ts";
    const RAW_RUNNING = /\b(?:process|ingest|proc|backupProcess)\??\.state\s*[!=]==\s*"running"/;
    const RAW_INGEST_TONE = /toneForState\(\s*ingest\??\.state/;

    const files = () =>
      sourceFiles(join(ROOT, "ui/src"), [".ts", ".tsx"]).filter((f) => f.slice(ROOT.length) !== MODULE);

    it("bans reading a process or ingest `state === \"running\"` outside the module", () => {
      const offenders = files()
        .filter((f) => RAW_RUNNING.test(withoutComments(readFileSync(f, "utf8"))))
        .map((f) => f.slice(ROOT.length));
      expect(
        offenders,
        "these files decide 'running' themselves; call lib/liveness (destinationOnAir, " +
          "destinationShownState, processRunning, ingestProcessUp) so every surface agrees",
      ).toEqual([]);
    });

    it("bans drawing the ingest tone from the process state alone", () => {
      const offenders = files()
        .filter((f) => RAW_INGEST_TONE.test(withoutComments(readFileSync(f, "utf8"))))
        .map((f) => f.slice(ROOT.length));
      expect(offenders, "use ingestSignalTone from lib/liveness").toEqual([]);
    });

    it("still matches what it bans", () => {
      // The control: without it a renamed field passes both rules vacuously.
      expect(RAW_RUNNING.test('d.process?.state === "running"')).toBe(true);
      expect(RAW_RUNNING.test('ingest.state !== "running"')).toBe(true);
      expect(RAW_RUNNING.test('// ingest?.state === "running"')).toBe(true);
      expect(withoutComments('// ingest?.state === "running"\nconst a = 1;')).not.toMatch(RAW_RUNNING);
      expect(RAW_INGEST_TONE.test("toneForState(ingest?.state)")).toBe(true);
      expect(files().length).toBeGreaterThan(100);
    });
  });
});
