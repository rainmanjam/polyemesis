/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import {
  attributableBitrate,
  audioTrackCount,
  ingestAttribution,
  ingestBitrateKbps,
  mergeStatusDestinations,
  processAbsence,
  programmeIngestLive,
  programmeSnapshot,
  relayRate,
} from "./dashboardFacts";
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
    expect(src).toContain('["Recorder", programmeStatus?.recorder, absentLabel(featureEnabled?.recording)]');
    expect(src).toContain('["Meters", programmeStatus?.meters, absentLabel(featureEnabled?.meters)]');
    // The hard-coded word this replaced, which was the operator's own setting
    // read back at them whatever the truth was.
    expect(src).not.toContain('["Recorder", programmeStatus?.recorder, "disabled"]');
    expect(src).not.toContain('["Meters", programmeStatus?.meters, "disabled"]');
  });

  it("labels the Ingest card with the programme the snapshot came from", () => {
    const src = dashboard();
    expect(src).toContain("ingestAttribution(sourceCount, source)");
    expect(src).toContain("{attribution.name}");
  });

  it("scopes the Ingest card to the selected programme rather than the last writer", () => {
    const src = dashboard();
    // The card's figures come from the held, programme-scoped snapshot...
    expect(src).toContain("programmeSnapshot(prev, status, programme)");
    expect(src).toContain("const ingest = programmeStatus?.ingest;");
    expect(src).toContain("const source = programmeStatus?.source;");
    // ...the publish address is the selected programme's, not the default's...
    expect(src).toMatch(/api\s*\.system\(programme\)/);
    expect(src).not.toMatch(/api\s*\.system\(\)/);
    // ...so the raw install-wide snapshot no longer feeds it,
    expect(src).not.toContain("const ingest = status?.ingest;");
    expect(src).not.toContain("const source = status?.source;");
    // and the note that said "these follow whoever reported last" is gone,
    // because that is no longer what the card does.
    expect(src).not.toContain('t("dash.ingestSharedFeed")');
  });

  it("sizes each card's track chips from its own programme's ingest, not a constant six", () => {
    expect(dashboard()).toContain("trackCount={trackCount(sourceOf(d.sourceId), d.sourceId)}");
    const card = readFileSync(join(ROOT, "ui/src/components/DestinationCard.tsx"), "utf8");
    expect(card).toContain("<TrackSummary tracks={dest.tracks} labels={trackLabels} count={trackCount} />");
  });

  it("renders the server's destination-hold reason", () => {
    // The one sentence that tells a held destination from a crashed one, which
    // the server computed for this screen and nothing in ui/src rendered.
    expect(dashboard()).toContain("<DestinationHoldNote hold={status?.destinationHold} />");
  });
});

describe("ingestBitrateKbps", () => {
  it("prefers the ingest process where there is one", () => {
    expect(ingestBitrateKbps("running", 4200, [{ kbps: 11 }], true)).toBe(4200);
  });

  it("falls back to the relay series for SRT, which has no ingest process", () => {
    // The regression this exists for: reconcileIngest returns early for SRT, so
    // ingest is null on a HEALTHY install and the card read "—" forever.
    expect(ingestBitrateKbps(undefined, undefined, [{ kbps: 3 }, { kbps: 5200 }], true)).toBe(5200);
  });

  it("says nothing rather than zero when no bytes are arriving", () => {
    // A printed 0 claims a running, empty stream. "—" claims nothing.
    expect(ingestBitrateKbps(undefined, undefined, [{ kbps: 0 }], false)).toBeNull();
    expect(ingestBitrateKbps(undefined, undefined, [], true)).toBeNull();
  });

  it("does not invent a number from a stale series once the stream has stopped", () => {
    expect(ingestBitrateKbps(undefined, undefined, [{ kbps: 9000 }], false)).toBeNull();
  });
});

describe("mergeStatusDestinations", () => {
  const snap = (id: number, dests: { id: number; sourceId: number }[]) => ({
    source: { id },
    destinations: dests,
  });

  it("keeps other programmes' destinations when one programme reports", () => {
    // The regression: three engines publish onto one socket in turn, and
    // last-writer-wins rendered 9, then 2, then 2 every two seconds.
    const a = snap(1, [{ id: 10, sourceId: 1 }, { id: 11, sourceId: 1 }]);
    const withA = { ...a, destinations: mergeStatusDestinations(null, a) };
    const b = snap(2, [{ id: 20, sourceId: 2 }]);
    const merged = mergeStatusDestinations(withA, b);
    expect(merged.map((d) => d.id).sort()).toEqual([10, 11, 20]);
  });

  it("replaces a programme's own list rather than appending to it", () => {
    const a = snap(1, [{ id: 10, sourceId: 1 }, { id: 11, sourceId: 1 }]);
    const withA = { ...a, destinations: mergeStatusDestinations(null, a) };
    const again = snap(1, [{ id: 10, sourceId: 1 }]);
    expect(mergeStatusDestinations(withA, again).map((d) => d.id)).toEqual([10]);
  });

  it("clears a programme that now reports none", () => {
    // The reason the fold keys on source.id and not on the rows: an engine with
    // no destinations sends an empty list, and there would be nothing in it to
    // say whose stale rows to drop.
    const a = snap(1, [{ id: 10, sourceId: 1 }]);
    const withA = { ...a, destinations: mergeStatusDestinations(null, a) };
    expect(mergeStatusDestinations(withA, snap(1, []))).toEqual([]);
  });

  it("falls back to the snapshot when it does not say whose it is", () => {
    const prev = { source: { id: 1 }, destinations: [{ id: 10, sourceId: 1 }] };
    const anon = { destinations: [{ id: 99, sourceId: 3 }] };
    expect(mergeStatusDestinations(prev, anon).map((d) => d.id)).toEqual([99]);
  });
});

describe("mergeStatusDestinations across the two snapshot shapes", () => {
  it("does not duplicate when a snapshot carries more programmes than it names", () => {
    // statusPayload sends the whole install labelled with ONE source.id, while
    // publishStatus sends one programme with the same label shape. Folding on
    // the label alone kept the other programmes' rows and re-added them: 13
    // destinations rendered as 17, which is how this was found.
    const scoped = {
      source: { id: 1 },
      destinations: [{ id: 1, sourceId: 1 }],
    };
    const withScoped = { ...scoped, destinations: mergeStatusDestinations(null, scoped) };
    const other = { source: { id: 2 }, destinations: [{ id: 2, sourceId: 2 }] };
    const both = { ...other, destinations: mergeStatusDestinations(withScoped, other) };
    expect(both.destinations).toHaveLength(2);

    const installWide = {
      source: { id: 1 },
      destinations: [{ id: 1, sourceId: 1 }, { id: 2, sourceId: 2 }],
    };
    const merged = mergeStatusDestinations(both, installWide);
    expect(merged).toHaveLength(2);
    expect(merged.map((d) => d.id).sort()).toEqual([1, 2]);
  });
})


/* THE INGEST CARD FOLLOWS THE PROGRAMME IN THE HEADER.
 *
 * Every engine publishes its status onto one install-wide socket, so the app's
 * `status` alternates between programmes. The card used to print whichever one
 * spoke last. programmeSnapshot holds the last frame that names the selected
 * programme, which is the same device failoverState already used. */
describe("programmeSnapshot", () => {
  const snap = (id: number, kbps: number) =>
    ({ source: { id, name: `P${id}` }, ingest: { progress: { bitrateKbps: kbps } } }) as unknown as Parameters<
      typeof programmeSnapshot
    >[1];

  it("takes a frame that names the selected programme", () => {
    const a = snap(1, 4000);
    expect(programmeSnapshot(null, a, 1)).toBe(a);
  });

  it("ignores another programme's frame and keeps the selected one's", () => {
    const a = snap(1, 4000);
    const b = snap(2, 900);
    expect(programmeSnapshot(a, b, 1)).toBe(a);
  });

  it("shows nothing rather than another programme's figures before the selected one reports", () => {
    expect(programmeSnapshot(null, snap(2, 900), 1)).toBeNull();
  });

  it("drops the held frame when the operator switches programme", () => {
    // The middle case: the held snapshot is Studio A's, the header now says
    // Studio B, and B has not spoken yet. Holding A would be the original bug
    // in a slower form.
    expect(programmeSnapshot(snap(1, 4000), snap(1, 4100), 2)).toBeNull();
    expect(programmeSnapshot(snap(1, 4000), null, 2)).toBeNull();
  });

  it("passes everything through while no programme is selected", () => {
    const b = snap(2, 900);
    expect(programmeSnapshot(null, b, null)).toBe(b);
  });
});

describe("relayRate", () => {
  it("has no rate from a first sample", () => {
    expect(relayRate(null, 1000, 0)).toEqual({ atMs: 0, rxBytes: 1000, kbps: null });
  });

  it("is the programme's own received bytes over time, in kbit/s", () => {
    const first = relayRate(null, 0, 0);
    // 1,250,000 bytes in 2 s = 5000 kbit/s.
    expect(relayRate(first, 1_250_000, 2000)?.kbps).toBe(5000);
  });

  it("waits for a second's worth of bytes rather than dividing by a burst", () => {
    // Status frames are also published on every process transition, so two
    // can land milliseconds apart.
    const first = relayRate(null, 0, 0);
    expect(relayRate(first, 50_000, 100)).toBe(first);
  });

  it("starts again when the counter goes backwards (the engine restarted)", () => {
    const first = relayRate(null, 9_000_000, 0);
    expect(relayRate(first, 10, 2000)).toEqual({ atMs: 2000, rxBytes: 10, kbps: null });
  });

  it("is nothing without a relay figure", () => {
    expect(relayRate({ atMs: 0, rxBytes: 1, kbps: 3 }, undefined, 2000)).toBeNull();
  });
});

describe("attributableBitrate", () => {
  const series = [{ kbps: 5200 }];

  it("uses the relay series on a single-programme install", () => {
    expect(attributableBitrate(1, series, null)).toBe(series);
    expect(attributableBitrate(null, series, 900)).toBe(series);
  });

  it("uses the programme's own relay rate on a multi-programme install, never the shared series", () => {
    // stats frames carry {system, bitrate} and no source id: the series belongs
    // to whichever engine published last, so printing it as the selected
    // programme's bitrate would be another show's number.
    expect(attributableBitrate(2, series, 900)).toEqual([{ kbps: 900 }]);
    expect(attributableBitrate(2, series, null)).toEqual([]);
  });
});

describe("programmeIngestLive", () => {
  const probed = { probed: true } as Pick<SourceInfo, "probed">;
  const unprobed = { probed: false } as Pick<SourceInfo, "probed">;

  it("is the series' answer on a single-programme install", () => {
    expect(programmeIngestLive(true, 1, null, null)).toBe(true);
    expect(programmeIngestLive(false, 1, probed, 900)).toBe(false);
  });

  it("is the selected programme's own probe and bytes on a multi-programme install", () => {
    expect(programmeIngestLive(false, 3, probed, 900)).toBe(true);
    // Another programme's bytes in the shared series do not make this one live.
    expect(programmeIngestLive(true, 3, probed, 0)).toBe(false);
    expect(programmeIngestLive(true, 3, probed, null)).toBe(false);
    expect(programmeIngestLive(true, 3, unprobed, 900)).toBe(false);
  });
});
