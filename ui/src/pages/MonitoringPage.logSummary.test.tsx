// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

/* The FFmpeg log used to open on every line, Follow on, so a real decode
 * failure sat underneath a wall of identical red lines. The log now opens on
 * the errors view, folds identical runs into a count, states a one-line
 * verdict above the log, and keeps the raw stream behind "Show raw output". */

const now = Date.now();
const ago = (s: number) => new Date(now - s * 1000).toISOString();

const logs = [
  ...Array.from({ length: 5 }, (_, i) => ({
    time: ago(10 - i),
    process: "preview",
    text: "decode_slice_header error",
    level: "error" as const,
  })),
  { time: ago(4), process: "meters", text: "level ceiling warning", level: "warning" as const },
];

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({
    system: null,
    bitrate: [],
    logs,
    status: null,
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

describe("FFmpeg log panel", () => {
  it("opens on the errors view with identical lines folded into a count", () => {
    render(<MonitoringPage />);
    expect(screen.getByText("×5")).toBeTruthy();
    expect(screen.queryByText("level ceiling warning")).toBeNull();
  });

  it("states a one-line verdict naming the process with the errors", () => {
    render(<MonitoringPage />);
    expect(screen.getByRole("status").textContent).toBe(
      "preview: 5 errors in the last 60 s",
    );
  });

  it("keeps the raw stream behind Show raw output", () => {
    render(<MonitoringPage />);
    expect(screen.queryByText("level ceiling warning")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Show raw output" }));
    expect(screen.getByText("level ceiling warning")).toBeTruthy();
  });

  it("narrows the view with the search box", () => {
    render(<MonitoringPage />);
    fireEvent.click(screen.getByRole("button", { name: "All" }));
    fireEvent.change(screen.getByRole("searchbox", { name: "Search log" }), {
      target: { value: "ceiling" },
    });
    expect(screen.getByText("level ceiling warning")).toBeTruthy();
    expect(screen.queryByText("×5")).toBeNull();
  });
});
