// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* A13, closed: a timeline row about ANOTHER programme's ingest links to
 * /monitoring?programme=<id>&process=ingest, and the page switches to that
 * programme before it shows the log. Every programme's ingest process is named
 * "ingest", so without the switch the link would open whichever programme
 * happened to be selected -- the wrong log, under the right name. */

const { live, selectProgramme } = vi.hoisted(() => {
  const selectProgramme = vi.fn();
  const live = {
    system: null,
    bitrate: [],
    logs: [
      { time: new Date().toISOString(), process: "ingest", text: "ingest: connection reset", level: "error" as const },
      { time: new Date().toISOString(), process: "dest:7", text: "dest: broken pipe", level: "error" as const },
    ],
    status: null,
    clearLogs: () => {},
    programme: 1 as number | null,
    programmeKnown: true,
    programmes: [{ id: 1, name: "Main" }, { id: 2, name: "Vertical" }],
    selectProgramme,
  };
  return { live, selectProgramme };
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
  selectProgramme.mockReset();
  live.programme = 1;
  live.programmeKnown = true;
});

const page = (path: string) => (
  <MemoryRouter initialEntries={[path]}>
    <MonitoringPage />
  </MemoryRouter>
);

describe("Monitoring follows ?programme= in its URL", () => {
  it("switches to the linked programme and lands on its ingest log", () => {
    render(page("/monitoring?programme=2&process=ingest#process-log"));
    expect(selectProgramme).toHaveBeenCalledWith(2);
    expect(selectProgramme).toHaveBeenCalledTimes(1);
    expect(screen.getByText(/connection reset/)).toBeTruthy();
    expect(screen.queryByText(/broken pipe/)).toBeNull();
  });

  it("does not re-select the programme already shown, or one the server does not list", () => {
    render(page("/monitoring?programme=1&process=ingest"));
    cleanup();
    render(page("/monitoring?programme=99&process=ingest"));
    cleanup();
    render(page("/monitoring?programme=two&process=ingest"));
    expect(selectProgramme).not.toHaveBeenCalled();
  });

  // The programme list is not known on the first render after a reload; a
  // switch decided then would be ignored by selectProgramme, and the link lost.
  it("waits for the programme list, then switches once", () => {
    live.programmeKnown = false;
    const { rerender } = render(page("/monitoring?programme=2&process=ingest"));
    expect(selectProgramme).not.toHaveBeenCalled();
    live.programmeKnown = true;
    rerender(page("/monitoring?programme=2&process=ingest"));
    expect(selectProgramme).toHaveBeenCalledWith(2);
    // The link is applied once. Once the operator moves to another programme
    // with the switcher, the parameter still in the URL must not drag them back.
    live.programme = 2;
    rerender(page("/monitoring?programme=2&process=ingest"));
    live.programme = 1;
    rerender(page("/monitoring?programme=2&process=ingest"));
    expect(selectProgramme).toHaveBeenCalledTimes(1);
  });
});
