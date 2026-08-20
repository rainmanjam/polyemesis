/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { audioTrackCount, ingestAttribution, processAbsence } from "./dashboardFacts";
import type { SourceInfo } from "./types";

/* The three claims the dashboard used to make without evidence.
 *
 * Each of these is a page decision extracted to lib, which is how this repo
 * tests a decision that lives in a page: the wiring is one expression, the
 * argument is here. */

const src = (over: Partial<SourceInfo> = {}): SourceInfo =>
  ({ id: 1, name: "Main", probed: false, tracks: null, ...over }) as SourceInfo;

describe("ingestAttribution", () => {
  it("names the programme when the install has more than one", () => {
    expect(ingestAttribution(2, src({ id: 7, name: "Vertical" }))).toEqual({ name: "Vertical" });
  });

  it("says nothing on a single-programme install, where the feed cannot be ambiguous", () => {
    expect(ingestAttribution(1, src({ name: "Main" }))).toBeNull();
  });

  it("says nothing while the programme count is unknown", () => {
    // A count that has not arrived is not evidence of one programme or of ten.
    expect(ingestAttribution(null, src({ name: "Main" }))).toBeNull();
  });

  it("says nothing when the snapshot carries no name to attribute it to", () => {
    expect(ingestAttribution(3, src({ name: "" }))).toBeNull();
    expect(ingestAttribution(3, null)).toBeNull();
  });
});

describe("audioTrackCount", () => {
  it("is a dash until the ingest has been probed", () => {
    // "Audio tracks 0" is the reading that gets a stream refused by every major
    // platform, and before the probe answers it is not a measurement at all.
    expect(audioTrackCount(src({ probed: false, tracks: null }))).toBe("—");
    expect(audioTrackCount(null)).toBe("—");
    expect(audioTrackCount(undefined)).toBe("—");
  });

  it("is the measured count once the probe has answered, zero included", () => {
    expect(audioTrackCount(src({ probed: true, tracks: [] }))).toBe(0);
    expect(
      audioTrackCount(src({ probed: true, tracks: [{}, {}] as unknown as SourceInfo["tracks"] })),
    ).toBe(2);
  });
});

describe("processAbsence", () => {
  it("calls a feature that is ON but not running idle, never disabled", () => {
    // The recorder the storage guard halted, and the meters in every pre-stream
    // minute. Printing the operator's own setting back at them for either says
    // "you turned this off" about a feature that is on.
    expect(processAbsence(true)).toBe("idle");
  });

  it("calls a feature that is off disabled", () => {
    expect(processAbsence(false)).toBe("disabled");
  });

  it("claims nothing while the settings read has not landed", () => {
    expect(processAbsence(null)).toBe("unknown");
    expect(processAbsence(undefined)).toBe("unknown");
  });
});

/* AND THAT THE PAGE ACTUALLY ASKS.
 *
 * A decision extracted to lib is only half a fix: the page has to route through
 * it, and a unit test of the function alone passes just as happily with the old
 * expression still inline. These pin the wiring, the way preset-drift.test.ts
 * pins the two copies of the platform catalogue against each other.
 */
const ROOT = new URL("../../../", import.meta.url).pathname;
const dashboard = () => readFileSync(join(ROOT, "ui/src/pages/Dashboard.tsx"), "utf8");

describe("Dashboard, wired to these decisions", () => {
  it("takes the audio track count from audioTrackCount(), not from the raw list", () => {
    const src = dashboard();
    expect(src).toContain("value={audioTrackCount(source)}");
    expect(src).not.toContain("source?.tracks?.length ?? 0");
  });

  it("takes each pipeline row's absent word from the feature's own setting", () => {
    const src = dashboard();
    expect(src).toContain('["Recorder", status?.recorder, absentLabel(featureEnabled?.recording)]');
    expect(src).toContain('["Meters", status?.meters, absentLabel(featureEnabled?.meters)]');
    // The hard-coded word this replaced, which was the operator's own setting
    // read back at them whatever the truth was.
    expect(src).not.toContain('["Recorder", status?.recorder, "disabled"]');
    expect(src).not.toContain('["Meters", status?.meters, "disabled"]');
  });

  it("labels the Ingest card with the programme the snapshot came from", () => {
    const src = dashboard();
    expect(src).toContain("ingestAttribution(sourceCount, source)");
    expect(src).toContain("{attribution.name}");
    expect(src).toContain('t("dash.ingestSharedFeed")');
  });

  it("renders the server's destination-hold reason", () => {
    // The one sentence that tells a held destination from a crashed one, which
    // the server computed for this screen and nothing in ui/src rendered.
    expect(dashboard()).toContain("<DestinationHoldNote hold={status?.destinationHold} />");
  });
});
