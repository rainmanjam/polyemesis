// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* The monitoring page's counts read one as one. The catalogue has no plural
 * rules, so count 1 has its own key: the log verdict said "1 errors in the last
 * 60 s" and the relay tile "1 subscribers". */

const now = Date.now();
const ago = (s: number) => new Date(now - s * 1000).toISOString();

let logs: { time: string; process: string; text: string; level: "error" }[] = [];
let status: unknown = null;

const errors = (n: number) =>
  Array.from({ length: n }, (_, i) => ({
    time: ago(10 - i),
    process: "preview",
    text: "decode_slice_header error",
    level: "error" as const,
  }));

const relayWith = (subscribers: number) => ({
  renditions: [],
  destinations: [],
  relay: {
    port: 9000,
    subscribers: Array.from({ length: subscribers }, (_, i) => ({ id: i + 1, name: `sub${i + 1}` })),
    rxPackets: 0, rxBytes: 0, txPackets: 0, dropped: 0, tsPackets: 0, tsLost: 0, discontinuities: 0, lossPercent: 0,
  },
  source: { id: 1, name: "Main", probed: true, tracks: [] },
});

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({
    system: null,
    bitrate: [],
    logs,
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
  logs = [];
  status = null;
});

const draw = () => render(<MemoryRouter><MonitoringPage /></MemoryRouter>);

describe("MonitoringPage: the log verdict reads one as one", () => {
  it("says 1 error", () => {
    logs = errors(1);
    draw();
    expect(screen.getByText("preview: 1 error in the last 60 s")).toBeTruthy();
  });

  it("says 2 errors", () => {
    logs = errors(2);
    draw();
    expect(screen.getByText("preview: 2 errors in the last 60 s")).toBeTruthy();
  });
});

describe("MonitoringPage: the relay tile reads one as one", () => {
  it("says 1 subscriber", () => {
    status = relayWith(1);
    draw();
    expect(screen.getByText("1 subscriber · port 9000")).toBeTruthy();
  });

  it("says 2 subscribers", () => {
    status = relayWith(2);
    draw();
    expect(screen.getByText("2 subscribers · port 9000")).toBeTruthy();
  });
});
