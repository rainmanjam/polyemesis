// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* THE MONITORING PAGE SHOWS ITS PARTS BEFORE ITS ANSWER. (A65, A66, A67, A68, A70, A73)
 *
 *   A65  The FFmpeg log sat in the 22rem left column, about 290px wide, while the
 *        right column was empty under Processes. The log is what an operator reads
 *        when something breaks, so it now runs full width below the grid.
 *
 *   A66  The page opened on five jargon tiles and no sentence saying whether the
 *        stream is healthy. A status line under the title now says so, and it
 *        names the processes that failed.
 *
 *   A67  Expert mode, which takes raw FFmpeg arguments, was always a full card
 *        at the bottom of an everyday page. It is closed behind a button by
 *        default; it does not mount until it is asked for.
 *
 *   A68  Before the first sample every tile read 0%, and an empty process list
 *        spun forever. Before data, a tile now reads a dash. An empty list after a
 *        real answer says so, and the spinner is for the first load only.
 *
 *   A70  A failed destination list in Expert mode was swallowed, leaving an empty
 *        picker with no reason. It now says so inline, with a retry.
 *
 *   A73  The process badge was hand-rolled from the signal tone. It is now the
 *        kit's StatusPill, read through processStatus, so a failed process is drawn
 *        as a cross (data-tone="bad") and a stopped one as a ring.
 *
 * State is held in vi.hoisted so the mock factories can reach it. */

const state = vi.hoisted(() => ({
  processes: [] as unknown[],
  destinationCalls: 0,
  failDestinations: false,
  cpuCeiling: 85,
  system: null as unknown,
  connected: false,
}));

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({
    system: state.system,
    connected: state.connected,
    bitrate: [],
    logs: [],
    status: null,
    clearLogs: () => {},
    programme: null,
    programmeKnown: true,
    programmes: [],
    selectProgramme: () => {},
  }),
  useStaleTracker: () => ({ stale: false, failures: 0, ok: () => {}, failed: () => {} }),
}));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      listProcesses: () => Promise.resolve(state.processes),
      processLogs: () => Promise.resolve({ lines: [] }),
      listDestinations: () => {
        state.destinationCalls += 1;
        return state.failDestinations
          ? Promise.reject(new Error("destinations unavailable"))
          : Promise.resolve([]);
      },
      listTimeline: () => Promise.resolve([]),
      jobsOverview: () => Promise.resolve({ policy: { cpuCeilingPercent: state.cpuCeiling } }),
    },
  };
});

const { ExpertPanel, MonitoringPage } = await import("./MonitoringPage");
const { translate } = await import("@/lib/i18n");

beforeAll(() => {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.releasePointerCapture = () => {};
  window.HTMLElement.prototype.setPointerCapture = () => {};
  window.HTMLElement.prototype.scrollIntoView = () => {};
});

afterEach(() => {
  cleanup();
  state.processes = [];
  state.destinationCalls = 0;
  state.failDestinations = false;
  state.cpuCeiling = 85;
  state.system = null;
  state.connected = false;
});

const proc = (name: string, pstate: string) => ({
  status: { name, state: pstate, pid: 0, uptimeSec: 0, restarts: 0, lastError: "" },
  command: `ffmpeg -i ${name}`,
});

describe("MonitoringPage: the log is read at full width (A65)", () => {
  it("does not place the FFmpeg log in the narrow 22rem timeline column", () => {
    render(<MemoryRouter><MonitoringPage /></MemoryRouter>);
    const log = document.getElementById("process-log");
    expect(log).not.toBeNull();
    expect(log!.closest('[class*="22rem"]')).toBeNull();
  });
});

describe("MonitoringPage: one sentence says whether the stream is healthy (A66)", () => {
  it("says how many processes failed, and where to look", async () => {
    state.processes = [proc("preview", "failed"), proc("ingest", "running")];
    render(<MemoryRouter><MonitoringPage /></MemoryRouter>);

    expect(
      await screen.findByText(translate("en", "mon.verdictProcessesFailed", { count: 1 })),
    ).toBeTruthy();
  });

  it("never says all is running while a process is reconnecting or stopped (A66)", async () => {
    state.processes = [proc("preview", "running"), proc("ingest", "reconnecting")];
    render(<MemoryRouter><MonitoringPage /></MemoryRouter>);

    expect(
      await screen.findByText(translate("en", "mon.verdictSomeNotRunning", { running: 1, count: 2 })),
    ).toBeTruthy();
    expect(screen.queryByText(translate("en", "mon.verdictAllRunning", { count: 2 }))).toBeNull();
  });

  it("says all is running when every process is up and nothing is failing", async () => {
    state.processes = [proc("preview", "running"), proc("ingest", "running")];
    render(<MemoryRouter><MonitoringPage /></MemoryRouter>);

    expect(
      await screen.findByText(translate("en", "mon.verdictAllRunning", { count: 2 })),
    ).toBeTruthy();
  });
});

describe("MonitoringPage: Expert mode is closed by default (A67)", () => {
  it("offers a button to open the raw FFmpeg arguments, and does not mount them", () => {
    render(<MemoryRouter><MonitoringPage /></MemoryRouter>);

    expect(screen.queryByText(translate("en", "i18nSweep.expertModeTitle"))).toBeNull();
    const open = screen.getByRole("button", { name: translate("en", "mon.expertShow") });
    expect(open.getAttribute("aria-expanded")).toBe("false");

    fireEvent.click(open);

    expect(screen.getByText(translate("en", "i18nSweep.expertModeTitle"))).toBeTruthy();
  });
});

describe("MonitoringPage: tiles and lists before the first answer (A68)", () => {
  it("shows a dash, not 0%, on the host CPU tile before any sample arrives", () => {
    render(<MemoryRouter><MonitoringPage /></MemoryRouter>);

    expect(screen.queryByText("0%")).toBeNull();
  });

  it("says no processes are running once the list has answered empty, with no spinner", async () => {
    render(<MemoryRouter><MonitoringPage /></MemoryRouter>);

    expect(await screen.findByText(translate("en", "mon.noProcesses"))).toBeTruthy();
    expect(document.querySelector(".animate-spin")).toBeNull();
  });
});

describe("ExpertPanel: a failed destination list is said, not swallowed (A70)", () => {
  it("shows the failure inline and retries when asked", async () => {
    state.failDestinations = true;
    render(<ExpertPanel />);

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain(translate("en", "mon.destinationsLoadFailed"));

    state.failDestinations = false;
    const before = state.destinationCalls;
    fireEvent.click(screen.getByRole("button", { name: translate("en", "common.retry") }));

    await waitFor(() => expect(state.destinationCalls).toBeGreaterThan(before));
  });
});

describe("MonitoringPage: process state uses the shared status pill (A73)", () => {
  it("draws a failed process as a cross in the kit's bad tone", async () => {
    state.processes = [proc("preview", "failed")];
    render(<MemoryRouter><MonitoringPage /></MemoryRouter>);

    await waitFor(() => expect(document.querySelector('[data-tone="bad"]')).not.toBeNull());
  });
});

describe("MonitoringPage: the verdict reads the whole page (2026-11 review, row 6)", () => {
  const host = (cpuPercent: number) => ({
    cpuPercent, procCpuPercent: 1, memUsedBytes: 1, memTotalBytes: 2, memPercent: 40, procMemBytes: 1, numCpu: 8,
  });
  const allRunning = () =>
    Array.from({ length: 12 }, (_, i) => proc(`p${i}`, "running"));

  it("does not say all running above a CPU warning", async () => {
    state.processes = allRunning();
    state.system = host(91);
    render(<MemoryRouter><MonitoringPage /></MemoryRouter>);
    expect(
      await screen.findByText(translate("en", "mon.verdictCpuHigh", { pct: 91, limit: 85 })),
    ).toBeTruthy();
    expect(screen.queryByText(translate("en", "mon.verdictAllRunning", { count: 12 }))).toBeNull();
  });

  it("says all running when nothing on the page warns", async () => {
    state.processes = allRunning();
    state.system = host(30);
    render(<MemoryRouter><MonitoringPage /></MemoryRouter>);
    expect(await screen.findByText(translate("en", "mon.verdictAllRunning", { count: 12 }))).toBeTruthy();
  });

  /* The sentence is a live region. It holds the percentage it quoted until
   * the figure moves 5 points, so a CPU hovering at 89-91% is not
   * re-announced on every host sample; the tile carries the exact figure. */
  it("holds the quoted percentage through small moves and re-words on a real one", async () => {
    state.processes = allRunning();
    state.system = host(91);
    const ui = () => <MemoryRouter><MonitoringPage /></MemoryRouter>;
    const { rerender } = render(ui());
    const at = (pct: number) => translate("en", "mon.verdictCpuHigh", { pct, limit: 85 });
    expect(await screen.findByText(at(91))).toBeTruthy();

    state.system = host(89);
    rerender(ui());
    expect(screen.getByText(at(91))).toBeTruthy();
    expect(screen.queryByText(at(89))).toBeNull();

    state.system = host(97);
    rerender(ui());
    expect(screen.getByText(at(97))).toBeTruthy();

    // A change of verdict re-words at once.
    state.system = host(30);
    rerender(ui());
    expect(screen.getByText(translate("en", "mon.verdictAllRunning", { count: 12 }))).toBeTruthy();
  });

  it("says the live figures are frozen once the socket drops, not before it first connects", async () => {
    state.processes = allRunning();
    state.system = host(30);
    const ui = () => <MemoryRouter><MonitoringPage /></MemoryRouter>;
    const { rerender } = render(ui());
    const offline = translate("en", "chrome.socketOfflineHint");
    expect(await screen.findByText(translate("en", "mon.verdictAllRunning", { count: 12 }))).toBeTruthy();
    expect(screen.queryByText(offline)).toBeNull();

    state.connected = true;
    rerender(ui());
    state.connected = false;
    rerender(ui());
    expect(screen.getByText(offline)).toBeTruthy();
  });

  it("still warns when no process is running at all", async () => {
    state.processes = [];
    state.system = host(91);
    render(<MemoryRouter><MonitoringPage /></MemoryRouter>);
    expect(
      await screen.findByText(translate("en", "mon.verdictCpuHigh", { pct: 91, limit: 85 })),
    ).toBeTruthy();
  });
});

describe("MonitoringPage: the CPU warning follows the Jobs ceiling (A71)", () => {
  it("names the saved Jobs CPU ceiling as the threshold on the CPU tile", async () => {
    state.cpuCeiling = 70;
    render(<MemoryRouter><MonitoringPage /></MemoryRouter>);

    expect(
      await screen.findByText(translate("en", "mon.cpuWarnsAbove", { pct: 70 })),
    ).toBeTruthy();
  });
});
