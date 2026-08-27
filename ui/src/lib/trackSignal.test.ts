/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { trackSignal, TRACK_SIGNAL_TEXT } from "./trackSignal";

/* THE METER COLUMN ON THE PAGE WHERE ROUTING IS DECIDED.
 *
 * Two strings for four situations, so two of them were false -- and "this
 * track carries nothing" is the reading that gets a track left out of the mix.
 */

describe("trackSignal", () => {
  it("does not say 'no signal' about a track nobody has measured", () => {
    // `probed` is the INGEST LAYOUT -- FFprobe has described the streams -- and
    // says nothing about whether the meters are running. With meters off,
    // every track on a healthy ingest read "no signal".
    expect(trackSignal({ hasLevels: false, probed: true, feedLive: true })).toBe(
      "notMeasured",
    );
    expect(TRACK_SIGNAL_TEXT.notMeasured).toBe("not measured");
  });

  it("distinguishes an ingest that has not been described yet", () => {
    expect(trackSignal({ hasLevels: false, probed: false, feedLive: true })).toBe("waiting");
  });

  it("refuses to draw a meter once the feed has gone, however fresh the numbers look", () => {
    // THE WORSE HALF: `levels` was never cleared on ws.onclose, so the last
    // frame before the disconnection stayed on screen bouncing at nothing and
    // read as live audio. A stale reading that looks healthy is worse than no
    // reading, because there is nothing to notice.
    expect(trackSignal({ hasLevels: true, probed: true, feedLive: false })).toBe("offline");
    expect(TRACK_SIGNAL_TEXT.offline).toBe("meter feed offline");
  });

  it("still draws the meter when the feed is up and the numbers are real", () => {
    expect(trackSignal({ hasLevels: true, probed: true, feedLive: true })).toBe("meter");
  });
});

/* AND THAT THE TWO HALVES ARE WIRED. */
const ROOT = new URL("../../../", import.meta.url).pathname;
const read = (p: string) => readFileSync(join(ROOT, p), "utf8");

describe("TrackRows", () => {
  const src = () => read("ui/src/components/signature/TrackRows.tsx");

  it("no longer decides the column from `probed` alone", () => {
    expect(src()).not.toContain('{probed ? "no signal" : "waiting for stream"}');
    expect(src()).toContain("const signal = trackSignal({");
    expect(src()).toContain("feedLive: meterFeedLive");
  });
});

describe("LiveDataProvider", () => {
  const src = () => read("ui/src/components/LiveDataProvider.tsx");

  it("clears the levels when the socket closes, so no frozen frame survives", () => {
    // Bounded by the END OF THE HANDLER, not by a character count. A fixed
    // window made this assertion a hostage of the comments above it: adding
    // one pushed `setLevels(null)` out of range and the test reported a
    // regression that had not happened.
    const from = src().slice(src().indexOf("ws.onclose = () => {"));
    const close = from.slice(0, from.indexOf("\n      };"));
    expect(
      close,
      "levels is a measurement of NOW; holding the last frame through a " +
        "reconnect draws stale audio as live",
    ).toContain("setLevels(null);");
  });
});

describe("RoutingPage", () => {
  it("hands the editor the meter feed's liveness", () => {
    expect(read("ui/src/pages/RoutingPage.tsx")).toContain("meterFeedLive={ctx.meterFeedLive}");
  });
});
