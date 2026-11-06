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
      translate("en", "state.stopped"),
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

/* app-council A81. The live badge sat beside this chip counting every running
 * destination on the install. With Studio A selected and two of its own
 * destinations live, three of Studio B's made the header read "5 live". The
 * badge now counts the selected programme and names it. */
describe("the header live badge counts and names the selected programme", () => {
  const dest = (id: number, sourceId: number, running: boolean) => ({
    id,
    sourceId,
    name: `d${id}`,
    enabled: true,
    process: { state: running ? "running" : "stopped", uptimeSec: running ? 10 : 0 },
  });

  it("reads 'Studio A: 2 live' when Studio B carries three more live destinations", () => {
    current = {
      ...frame(1, "Studio A", true, 1_000_000, 10),
      destinations: [dest(1, 1, true), dest(2, 1, true), dest(3, 1, false), dest(4, 2, true), dest(5, 2, true), dest(6, 2, true)],
    } as unknown as Status;
    const { container } = render(layout());

    const scoped = translate("en", "chrome.liveCountProgramme", { programme: "Studio A", count: 2 });
    expect(container.textContent, "the badge does not name and scope to Studio A").toContain(scoped);
    expect(screen.getByTestId("mobile-on-air").textContent).toContain(scoped);
    expect(container.textContent, "the install-wide count is back").not.toContain(
      translate("en", "chrome.liveCount", { count: 5 }),
    );
  });

  // At 390px "Main: Off air" wrapped onto three lines beside the logo, and
  // later, truncated, it read "O…" at 360px. jsdom cannot measure either, so
  // this holds the markup that prevents both; the e2e spec
  // header-programme-once measures the real chip at 320, 360 and 390px.
  it("keeps the phone chip on one line and prints its words whole, never truncated", () => {
    current = {
      ...frame(1, "A programme with a long name", true, 1_000_000, 10),
      destinations: [dest(1, 1, false)],
    } as unknown as Status;
    render(layout());
    const chip = screen.getByTestId("mobile-on-air");
    expect(chip.className).toContain("whitespace-nowrap");
    // Never below the status dot (min-w-1.5, the dot's 6px), and in the
    // programme-scoped case it does not shrink at all: the switcher gives way.
    expect(chip.className.split(/\s+/)).toContain("min-w-1.5");
    expect(chip.className.split(/\s+/)).toContain("shrink-0");
    const words = screen.getByTestId("mobile-on-air-words");
    expect(words.className).not.toContain("truncate");
    // jsdom has no ResizeObserver, so the chip prints the full words.
    expect(words.getAttribute("data-fit")).toBe("full");
    expect(words.textContent).toBe(translate("en", "onAir.offAir"));
    // The whole scoped sentence is in the title and, for a screen reader, in
    // the sr-only text; the visible part is the state alone, because the
    // programme switcher beside it already names the programme (2026-11).
    const sr = chip.querySelector(".sr-only")?.textContent ?? "";
    expect(chip.getAttribute("title")).toBe(sr);
    expect(sr).toMatch(/^.+: /); // "<programme>: Off air"
  });
});

/* A COLD LOAD IS NOT A STOPPED INGEST. Until the page has what it decides
 * liveness from -- the first status frame, and on a multi-programme install
 * two of the programme's frames a second apart for a relay rate -- it has no
 * reading at all. The chip printed "Stopped" for that second or two beside a
 * live broadcast (the capture tooling had to wait it out), and the phone's
 * on-air chip printed "Off air" before any frame had arrived. */
describe("the header's status chips before their first reading", () => {
  const loading = translate("en", "common.loading");

  it("reads loading, not Stopped or Off air, before the first status frame", () => {
    current = null;
    render(layout());
    expect(chip()).toBe(loading);
    const phone = screen.getByTestId("mobile-on-air");
    expect(phone.textContent).toContain(loading);
    expect(phone.textContent).not.toContain(translate("en", "onAir.offAir"));
  });

  it("reads loading on one frame of a probed SRT programme, then decides on the second", () => {
    seriesLive = false;
    current = frame(1, "Studio A", true, 1_000_000);
    const { rerender } = render(layout());
    expect(chip(), "one frame has no relay rate yet").toBe(loading);

    // A second later, the counter has not moved: now "not receiving" is a
    // verdict, and Stopped is the truth.
    vi.setSystemTime(new Date("2026-10-30T12:00:01Z"));
    current = frame(1, "Studio A", true, 1_000_000);
    rerender(layout());
    expect(chip()).toBe(translate("en", "state.stopped"));
  });

  it("shows a process state the server reported, even before a rate exists", () => {
    seriesLive = false;
    current = {
      ...frame(1, "Studio A", true, 1_000_000),
      ingest: { state: "failed" },
    } as unknown as Status;
    render(layout());
    expect(chip()).toBe(translate("en", "state.failed"));
  });
});
