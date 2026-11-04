// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { setLanguage } from "@/lib/i18n";

/* The host and ingest tiles read one as one. The catalogue has no plural rules:
 * the CPU tile has its own key for a single core ("1 cores" before), and the
 * TS-loss line was rewritten to "label: value" so "1 breaks" cannot happen. */

let status: unknown = null;
let system: unknown = null;

const host = (numCpu: number) => ({
  cpuPercent: 30,
  procCpuPercent: 12,
  memUsedBytes: 1024,
  memTotalBytes: 4096,
  memPercent: 25,
  procMemBytes: 512,
  numCpu,
});

/** A relay that has seen `lost` TS packets go missing out of `packets` that
 *  arrived, with `breaks` continuity breaks. Kept under 1000 so the numbers
 *  carry no locale-dependent grouping separator. */
const relayLoss = (lost: number, packets: number, breaks: number) => ({
  renditions: [],
  destinations: [],
  relay: {
    port: 9000,
    subscribers: [],
    rxPackets: 0, rxBytes: 0, txPackets: 0, dropped: 0,
    tsPackets: packets, tsLost: lost, discontinuities: breaks, lossPercent: 0,
  },
  source: { id: 1, name: "Main", probed: true, tracks: [] },
});

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({
    system,
    bitrate: [],
    logs: [],
    status,
    clearLogs: vi.fn(),
    programme: null,
    programmeKnown: true,
  }),
  useStaleTracker: () => ({ stale: false, failures: 0, ok: vi.fn(), failed: vi.fn() }),
}));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      listProcesses: vi.fn().mockResolvedValue([]),
      processLogs: vi.fn().mockResolvedValue({ lines: [] }),
      listDestinations: vi.fn().mockResolvedValue([]),
      listTimeline: vi.fn().mockResolvedValue([]),
    },
  };
});

import { MonitoringPage } from "./MonitoringPage";

beforeAll(() => {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.releasePointerCapture = () => {};
  window.HTMLElement.prototype.setPointerCapture = () => {};
  window.HTMLElement.prototype.scrollIntoView = () => {};
});

afterEach(() => {
  cleanup();
  setLanguage("en");
  status = null;
  system = null;
});

const draw = () => render(<MemoryRouter><MonitoringPage /></MemoryRouter>);

describe("MonitoringPage: the CPU tile reads one core as one (en)", () => {
  it("says 1 core", () => {
    setLanguage("en");
    system = host(1);
    draw();
    expect(screen.getByText("1 core · polyemesis 12%")).toBeTruthy();
    expect(screen.queryByText("1 cores · polyemesis 12%")).toBeNull();
  });

  it("says 2 cores", () => {
    setLanguage("en");
    system = host(2);
    draw();
    expect(screen.getByText("2 cores · polyemesis 12%")).toBeTruthy();
  });
});

describe("MonitoringPage: the TS-loss line is count-neutral (en)", () => {
  it("reads TS packets missing: 1 of 10 · breaks: 1", () => {
    // The total is what arrived plus what went missing: 9 + 1.
    setLanguage("en");
    status = relayLoss(1, 9, 1);
    draw();
    expect(screen.getByText("TS packets missing: 1 of 10 · breaks: 1")).toBeTruthy();
    expect(screen.queryByText("1 of 10 TS packets missing · 1 breaks")).toBeNull();
  });

  it("reads TS packets missing: 2 of 100 · breaks: 2", () => {
    setLanguage("en");
    status = relayLoss(2, 98, 2);
    draw();
    expect(screen.getByText("TS packets missing: 2 of 100 · breaks: 2")).toBeTruthy();
  });
});
