// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* A9 on the Monitoring page: a failure reason, the log summary and a tile in
 * its warn tone each come with the remedy and one control that carries it out,
 * and a "Show log" link from elsewhere lands on the log filtered to its
 * process. */

const { ago, live } = vi.hoisted(() => {
  const now = Date.now();
  const ago = (s: number) => new Date(now - s * 1000).toISOString();
  const live = {
    system: {
      cpuPercent: 97, numCpu: 4, procCpuPercent: 80,
      memPercent: 40, memUsedBytes: 1, memTotalBytes: 2, procMemBytes: 1,
    },
    bitrate: [],
    logs: [
      { time: ago(5), process: "dest:7", text: "Error opening output rtmp://x/live/****: I/O error", level: "error" as const },
      { time: ago(4), process: "preview", text: "decode_slice_header error", level: "error" as const },
    ],
    status: {
      relay: { rxBytes: 0, subscribers: [], port: 1, dropped: 0, lossPercent: 1.5, tsLost: 10, tsPackets: 990, discontinuities: 0 },
    },
    clearLogs: () => {},
    programme: null,
    programmeKnown: true,
  };
  return { ago, live };
});

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => live,
  useStaleTracker: () => ({ stale: false, failures: 0, ok: vi.fn(), failed: vi.fn() }),
}));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      listProcesses: vi.fn().mockResolvedValue([
        {
          status: {
            name: "dest:7", kind: "destination", state: "failed", pid: 0, restarts: 2, uptimeSec: 0,
            progress: {}, lastError: "exit status 251: …",
            reason: { cause: "key_rejected", held: true, rejections: 3, at: ago(3) },
          },
          command: "ffmpeg …",
        },
      ]),
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

afterEach(() => cleanup());

const draw = (path = "/monitoring") =>
  render(
    <MemoryRouter initialEntries={[path]}>
      <MonitoringPage />
    </MemoryRouter>,
  );

describe("Monitoring: every failure comes with its remedy", () => {
  it("a process with a classified reason names it, says what to check, and offers its log", async () => {
    draw();
    // A72: the process is a table row now; its reason is in the row's detail,
    // one click on the row's toggle away.
    const toggle = await screen.findByRole("button", { name: "Details for dest:7" });
    fireEvent.click(toggle);
    await waitFor(() => expect(screen.getByText("Stream key rejected")).toBeTruthy());
    expect(screen.getByText(/Check the stream key in the platform's dashboard/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Show log" })).toBeTruthy();
  });

  it("the log summary offers the log of the process it names", () => {
    draw();
    const show = screen.getByRole("button", { name: /^Show .+ log$/ });
    const named = /^Show (.+) log$/.exec(show.getAttribute("aria-label") ?? show.textContent ?? "")![1];
    expect(screen.getByRole("status").textContent).toContain(named);
    fireEvent.click(show);
    // Filtered to that process, the offer has done its job and goes away.
    expect(screen.queryByRole("button", { name: `Show ${named} log` })).toBeNull();
  });

  it("a tile in its warn tone carries a remedy and a link to where it is fixed", () => {
    draw();
    const tiles = screen.getAllByTestId("tile-remedy");
    const text = tiles.map((t) => t.textContent).join(" | ");
    expect(text).toContain("The CPU is near its limit");
    expect(text).toContain("Packets are being lost");
    expect(screen.getByRole("link", { name: "Sources" }).getAttribute("href")).toBe("/sources");
  });

  it("a healthy tile carries no advice", () => {
    draw();
    expect(screen.queryByText(/Memory is nearly full/)).toBeNull();
  });

  it("a deep link lands on the log already filtered to its process", () => {
    draw("/monitoring?process=dest%3A7");
    expect(screen.getByText(/Error opening output/)).toBeTruthy();
    expect(screen.queryByText("decode_slice_header error")).toBeNull();
  });
});
