import { describe, expect, it } from "vitest";

import {
  formatHealthValue,
  hasMeasurements,
  healthRows,
  reportingStreamCount,
} from "./stream-health";
import {
  FACEBOOK_STREAM_HEALTH_INTERVAL_MS,
  FACEBOOK_STREAM_TIMEOUT_MS,
} from "./types";

/* The rule under test is one sentence and it is the reason the feature exists:
   a measurement Facebook did not send must not arrive at the operator as a
   number. Everything below is that sentence from a different angle. */

describe("healthRows", () => {
  it("produces no row for a stream that carried no health at all", () => {
    // The failure this forbids: a pane that draws Bitrate 0 and Frame rate 0
    // for an ingest Facebook simply has not described yet — a broadcast between
    // Go Live and the first byte, reported as an encoder that has died.
    expect(healthRows({ id: "1" })).toEqual([]);
    expect(healthRows({ id: "1", health: null })).toEqual([]);
    expect(healthRows({ id: "1", health: {} })).toEqual([]);
    expect(hasMeasurements({ id: "1" })).toBe(false);
  });

  it("keeps a zero Facebook actually sent", () => {
    // The mirror image, and the one a defensive filter gets wrong: a reported
    // 0 fps is a real reading about a stalled ingest and is the most useful
    // number on the pane. Absent and zero are two states; both survive.
    const rows = healthRows({ id: "1", health: { framerate: 0 } });
    expect(rows).toEqual([{ name: "framerate", value: 0 }]);
    expect(hasMeasurements({ id: "1", health: { framerate: 0 } })).toBe(true);
  });

  it("renders whatever keys arrived rather than a fixed set of ours", () => {
    // Facebook's key spellings are not published, so the pane cannot declare
    // rows in advance. Two unfamiliar names still both reach the screen.
    const rows = healthRows({
      id: "1",
      health: { video_bitrate: 5800, some_field_nobody_documented: 12 },
    });
    expect(rows.map((r) => r.name)).toEqual([
      "some_field_nobody_documented",
      "video_bitrate",
    ]);
  });

  it("sorts by name so two identical polls draw the same order", () => {
    // Object key order is not a contract. A list that reshuffles between two
    // reads two seconds apart reads as a change in the stream.
    const rows = healthRows({ id: "1", health: { zeta: 1, alpha: 2, mid: 3 } });
    expect(rows.map((r) => r.name)).toEqual(["alpha", "mid", "zeta"]);
  });

  it("drops non-finite values instead of drawing NaN at an operator", () => {
    const rows = healthRows({
      id: "1",
      health: { good: 42, bad: Number.NaN, worse: Number.POSITIVE_INFINITY },
    });
    expect(rows).toEqual([{ name: "good", value: 42 }]);
  });
});

describe("formatHealthValue", () => {
  it("leaves integers as integers", () => {
    expect(formatHealthValue(30)).toBe("30");
    expect(formatHealthValue(0)).toBe("0");
  });

  it("caps fractions so a float artefact cannot resize the row mid-poll", () => {
    expect(formatHealthValue(29.970029970029973)).toBe("29.97");
    expect(formatHealthValue(59.5)).toBe("59.5");
  });
});

describe("reportingStreamCount", () => {
  it("treats absent and empty alike, because both mean no ingest to end", () => {
    expect(reportingStreamCount(undefined)).toBe(0);
    expect(reportingStreamCount(null)).toBe(0);
    expect(reportingStreamCount([])).toBe(0);
    expect(reportingStreamCount([{ id: "a" }, { id: "b" }])).toBe(2);
  });
});

describe("Facebook's published timings", () => {
  it("carries Facebook's own numbers and not a rounder pair of ours", () => {
    // Meta's Broadcasting guide, read 2026-08-16: "Stream health data refreshes
    // every 2 seconds, so limit queries to no more than once every 2 seconds. A
    // stream timeout will be detected and reported after 4 seconds of no data
    // being received."
    //
    // Asserted rather than trusted because the poll loop's pace is the one
    // thing in this feature that a platform can rate-limit us for, and "someone
    // made it 1000 to feel snappier" is a change no other test would notice.
    expect(FACEBOOK_STREAM_HEALTH_INTERVAL_MS).toBe(2000);
    expect(FACEBOOK_STREAM_TIMEOUT_MS).toBe(4000);
  });
});
