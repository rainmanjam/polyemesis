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

function tsxFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...tsxFiles(p));
    else if (entry.name.endsWith(".tsx") && !entry.name.includes(".test.")) out.push(p);
  }
  return out;
}

describe("ingest liveness", () => {
  it("is never labelled from the process state alone", () => {
    const offenders: string[] = [];
    for (const file of tsxFiles(join(ROOT, "ui/src"))) {
      const src = readFileSync(file, "utf8");
      if (!src.includes("stateLabel(ingest?.state)")) continue;
      if (!src.includes("useIngestLive(")) {
        offenders.push(file.slice(ROOT.length));
      }
    }
    expect(
      offenders,
      "these files label the ingest from status.ingest alone, which reads " +
        "Offline on a healthy SRT install -- use useIngestLive (see #514)",
    ).toEqual([]);
  });

  it("still finds the call sites it is meant to guard", () => {
    // Without this the rule above passes trivially the day someone renames
    // stateLabel or the ingest field, and the guard silently stops guarding.
    const guarded = tsxFiles(join(ROOT, "ui/src")).filter((f) =>
      readFileSync(f, "utf8").includes("stateLabel(ingest?.state)"),
    );
    expect(guarded.length).toBeGreaterThan(0);
  });
});
