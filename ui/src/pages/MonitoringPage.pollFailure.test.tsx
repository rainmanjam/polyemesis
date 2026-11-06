// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* The process poll hands its failure to the staleness tracker. A rejected
 * listProcesses must reach it (and nothing else), so the page marks itself
 * stale rather than throwing an unhandled rejection. */

const failed = vi.hoisted(() => vi.fn());

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({
    system: null,
    bitrate: [],
    logs: [],
    status: null,
    clearLogs: () => {},
    programme: null,
    programmeKnown: true,
    programmes: [],
    selectProgramme: () => {},
  }),
  useStaleTracker: () => ({ stale: false, failures: 0, ok: () => {}, failed }),
}));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      listProcesses: () => Promise.reject(new Error("down")),
      processLogs: () => Promise.resolve({ lines: [] }),
      listDestinations: () => Promise.resolve([]),
      listTimeline: () => Promise.resolve([]),
      jobsOverview: () => Promise.resolve({ policy: { cpuCeilingPercent: 85 } }),
    },
  };
});

const { MonitoringPage } = await import("./MonitoringPage");

afterEach(cleanup);

describe("MonitoringPage process poll", () => {
  it("routes a rejected poll to the stale tracker", async () => {
    window.HTMLElement.prototype.scrollIntoView = () => {};
    render(
      <MemoryRouter>
        <MonitoringPage />
      </MemoryRouter>,
    );
    await waitFor(() => expect(failed).toHaveBeenCalled());
  });
});
