// @vitest-environment jsdom
//
// THE HEADER'S INGEST CHIP IS ABOUT THE PROGRAMME SELECTED IN THE HEADER.
//
// The status socket is install-wide: every engine publishes onto it and the
// app keeps the last frame. The dashboard's Ingest card was scoped to the
// selected programme in #924 (programmeSnapshot, relayRate,
// programmeIngestLive); the chip in the header beside the programme switcher
// was not. It read `status.ingest` off whichever frame landed last and
// decided "live" from the `stats` bitrate series, which names no programme --
// so with Studio A selected and idle, Studio B's encoder turned Studio A's
// chip green and put Studio B's uptime beside it.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { translate } from "@/lib/i18n";
import type { Status } from "@/lib/types";

const relay = (rxBytes: number, receivingSec?: number) => ({
  port: 0,
  subscribers: [],
  rxPackets: 0,
  rxBytes,
  txPackets: 0,
  dropped: 0,
  tsPackets: 0,
  tsLost: 0,
  discontinuities: 0,
  lossPercent: 0,
  ...(receivingSec === undefined ? {} : { receivingSec }),
});

/** An SRT programme's frame: no ingest process, by design. */
const frame = (id: number, name: string, probed: boolean, rxBytes: number, receivingSec?: number) =>
  ({
    destinations: [],
    renditions: [],
    relay: relay(rxBytes, receivingSec),
    source: { id, name, probed, tracks: [] },
  }) as unknown as Status;

let current: Status | null = null;
// The unscoped `stats` series: bytes are arriving SOMEWHERE on the install.
let seriesLive = true;

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({
    status: current,
    connected: true,
    frameError: false,
    source: current?.source ?? null,
    bitrate: [{ t: 0, kbps: 6000 }],
    levels: null,
    system: null,
    logs: [],
    programme: 1,
    programmeKnown: true,
    snapshotKnown: true,
    sourceCount: 2,
    programmes: [
      { id: 1, name: "Studio A" },
      { id: 2, name: "Studio B" },
    ],
    selectProgramme: () => {},
    refreshSources: async () => {},
    recordingsRevision: 0,
  }),
  useIngestLive: () => seriesLive,
}));

import { AppLayout } from "./AppLayout";

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(new Date("2026-10-30T12:00:00Z"));
  seriesLive = true;
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

const layout = () => (
  <MemoryRouter>
    <AppLayout username="admin" onSignOut={() => {}} />
  </MemoryRouter>
);

const chip = () => screen.getByTestId("chrome-ingest-status").textContent ?? "";

describe("the header ingest chip follows the programme selected in the header", () => {
  it("does not read live, or show an uptime, from another programme's frame and bytes", () => {
    // Studio A (selected) is idle; Studio B is receiving, and spoke last.
    current = frame(1, "Studio A", false, 0);
    const { rerender, container } = render(layout());
    vi.setSystemTime(new Date("2026-10-30T12:00:02Z"));
    current = frame(2, "Studio B", true, 9_000_000, 754);
    rerender(layout());

    expect(chip(), "Studio B's bytes turned Studio A's chip live").toBe(
      translate("en", "state.offline"),
    );
    expect(container.textContent, "Studio B's uptime is in Studio A's header").not.toContain(
      "00:12:34",
    );
  });

  it("reads live with the stream's arrival uptime when the selected programme is receiving", () => {
    // Studio A is receiving over SRT -- no ingest process -- and its relay
    // counter moves between two of its own frames; Studio B's frame lands in
    // between and must not reset or replace that.
    seriesLive = false;
    current = frame(1, "Studio A", true, 1_000_000, 752);
    const { rerender, container } = render(layout());
    vi.setSystemTime(new Date("2026-10-30T12:00:01Z"));
    current = frame(2, "Studio B", false, 0);
    rerender(layout());
    vi.setSystemTime(new Date("2026-10-30T12:00:02Z"));
    current = frame(1, "Studio A", true, 2_500_000, 754);
    rerender(layout());

    expect(chip()).toBe(translate("en", "state.live"));
    expect(container.textContent, "the SRT ingest's uptime still reads nothing").toContain(
      "00:12:34",
    );
  });
});
