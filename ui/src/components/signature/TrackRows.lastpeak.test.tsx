// @vitest-environment jsdom

import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { TrackRows } from "./TrackRows";
import type { SourceTrack, TrackSel } from "@/lib/types";

/* A10: a track whose meter has gone quiet must not read as blank. It keeps its
 * last measured peak, with the time it was measured, as text. That is the
 * reading an operator needs when the feed drops mid-show: what the track was
 * doing, and when, rather than a frozen bar or nothing. It is text, never a
 * meter bar, so a stale value cannot pass for a live one. */
afterEach(cleanup);

// jsdom has no ResizeObserver, and the gain slider needs one to mount.
beforeAll(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
});
afterAll(() => vi.unstubAllGlobals());

const tracks: SourceTrack[] = [{ index: 0, channels: 2, codec: "aac", layout: "stereo" }];
const selection: TrackSel[] = [{ track: 0, enabled: true, gain: 1 }];
const measured = { peak: [[-6.3, -7]], rms: [[-20, -21]] };

describe("TrackRows last measured peak", () => {
  it("keeps the last peak as text when the meter feed drops", () => {
    const { rerender } = render(
      <TrackRows
        idPrefix="t"
        tracks={tracks}
        selection={selection}
        levels={measured}
        probed
        meterFeedLive
        onChange={() => {}}
      />,
    );
    rerender(
      <TrackRows
        idPrefix="t"
        tracks={tracks}
        selection={selection}
        levels={null}
        probed
        meterFeedLive={false}
        onChange={() => {}}
      />,
    );
    expect(screen.getByText("meter feed offline")).toBeTruthy();
    expect(screen.getByText(/last peak -6\.3 dB at /)).toBeTruthy();
  });

  it("offers no last peak for a track that has never been measured", () => {
    render(
      <TrackRows
        idPrefix="t"
        tracks={tracks}
        selection={selection}
        levels={null}
        probed
        meterFeedLive
        onChange={() => {}}
      />,
    );
    expect(screen.getByText("not measured")).toBeTruthy();
    expect(screen.queryByText(/last peak/)).toBeNull();
  });

  it("never records a frame that arrives while the feed is offline", () => {
    // A frozen last frame must not become a "last measured" reading: the
    // liveness rule has to hold for the history as well as for the bars.
    const offline = (frame: typeof measured) => (
      <TrackRows
        idPrefix="t"
        tracks={tracks}
        selection={selection}
        levels={frame}
        probed
        meterFeedLive={false}
        onChange={() => {}}
      />
    );
    const { rerender } = render(offline(measured));
    // The next frame arrives and the row renders again, as it does on the feed.
    rerender(offline({ peak: [[-6.3, -7]], rms: [[-20, -21]] }));
    expect(screen.queryByText(/last peak/)).toBeNull();
  });
});
