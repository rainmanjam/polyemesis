// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* The Monitoring process list is a compact table (app-council A72).
 *
 * It was one card per process: a dot, the name, a pill, three stat tiles, the
 * reason and a "command line" disclosure, about 110px each. Eleven processes
 * filled two screens, and a failed one sat wherever the supervisor listed it.
 * It is now one row per process -- Process, State, Restarts, Uptime, Errors --
 * with failed and reconnecting rows first, and each row expands to everything
 * the card held. Nothing on the card is dropped: the last describe below lists
 * every piece of it and finds each one in an expanded row.
 *
 * State is held in vi.hoisted so the mock factories can reach it. */

const state = vi.hoisted(() => {
  const now = new Date().toISOString();
  return {
    processes: [] as unknown[],
    logs: [
      { time: now, process: "dest:7", text: "dest7: broken pipe", level: "error" as const },
      { time: now, process: "ingest", text: "ingest: connection reset", level: "error" as const },
    ],
  };
});

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({
    system: null,
    bitrate: [],
    logs: state.logs,
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
      listDestinations: () => Promise.resolve([]),
      listTimeline: () => Promise.resolve([]),
      jobsOverview: () => Promise.resolve({ policy: { cpuCeilingPercent: 85 } }),
    },
  };
});

const { MonitoringPage } = await import("./MonitoringPage");
const { translate } = await import("@/lib/i18n");
const { processStatus } = await import("@/lib/status");
const en = (key: Parameters<typeof translate>[1], vars?: Record<string, string | number>) =>
  translate("en", key, vars);

beforeAll(() => {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.releasePointerCapture = () => {};
  window.HTMLElement.prototype.setPointerCapture = () => {};
  window.HTMLElement.prototype.scrollIntoView = () => {};
});

afterEach(() => {
  cleanup();
  state.processes = [];
});

interface Opts {
  pid?: number;
  uptimeSec?: number;
  restarts?: number;
  lastError?: string;
  nextRetryIn?: number;
  reason?: { cause: string; held: boolean; rejections: number; at: string };
}

const proc = (name: string, pstate: string, o: Opts = {}) => ({
  status: {
    name,
    state: pstate,
    pid: o.pid ?? 0,
    uptimeSec: o.uptimeSec ?? 0,
    restarts: o.restarts ?? 0,
    lastError: o.lastError ?? "",
    nextRetryIn: o.nextRetryIn,
    reason: o.reason,
  },
  command: `ffmpeg -i rtmp://relay/${name} -f flv out-${name}`,
});

const draw = () =>
  render(
    <MemoryRouter>
      <MonitoringPage />
    </MemoryRouter>,
  );

/** The process table, once the first poll has filled it. */
async function table() {
  return screen.findByRole("table", { name: en("procTable.caption") });
}

/** Body rows that name a process -- the detail rows have no row header. */
function processRows(t: HTMLElement) {
  return within(t)
    .getAllByRole("row")
    .filter((r) => within(r).queryAllByRole("rowheader").length > 0);
}

function toggleFor(name: string) {
  return screen.getByRole("button", { name: en("procTable.toggle", { process: name }) });
}

describe("Monitoring processes: a compact table (A72)", () => {
  it("renders the processes as a table with Process, State, Restarts, Uptime and Errors columns", async () => {
    state.processes = [proc("ingest", "running", { uptimeSec: 720 })];
    draw();

    const t = await table();
    const headers = within(t).getAllByRole("columnheader").map((h) => h.textContent?.trim());
    expect(headers).toEqual([
      en("procTable.colProcess"),
      en("procTable.colState"),
      en("mon.restarts"),
      en("mon.uptime"),
      en("procTable.colErrors"),
    ]);
    // Every column header is scoped to its column, and each process row has
    // its name as the row header, so a screen reader announces both.
    for (const h of within(t).getAllByRole("columnheader")) {
      expect(h.getAttribute("scope")).toBe("col");
    }
    expect(within(t).getByRole("rowheader").getAttribute("scope")).toBe("row");
    // The process card it replaces is gone: no stat tile labelled PID on the row.
    expect(within(t).queryByText(en("mon.pid"))).toBeNull();
  });

  it("sorts failed and reconnecting processes above the rest, keeping the rest in their order", async () => {
    state.processes = [
      proc("ingest", "running"),
      proc("preview", "running"),
      proc("dest:7", "failed"),
      proc("recorder", "stopped"),
      proc("dest:9", "reconnecting"),
    ];
    draw();

    const t = await table();
    await waitFor(() => expect(processRows(t)).toHaveLength(5));
    const names = processRows(t).map((r) => within(r).getByRole("rowheader").textContent?.trim());
    expect(names).toEqual(["dest:7", "dest:9", "ingest", "preview", "recorder"]);
  });

  it("draws the State cell with the kit's status pill", async () => {
    state.processes = [proc("ingest", "running"), proc("dest:7", "failed")];
    draw();

    const t = await table();
    await waitFor(() => expect(processRows(t)).toHaveLength(2));
    const [failed, running] = processRows(t);
    expect(failed.querySelector('[data-tone="bad"]')?.textContent).toContain(en(processStatus("failed").labelKey));
    expect(running.querySelector('[data-tone="ok"]')?.textContent).toContain(en(processStatus("running").labelKey));
  });

  it("shows restarts, uptime only while running, and the error count from the log", async () => {
    state.processes = [
      proc("dest:7", "failed", { restarts: 3, uptimeSec: 999 }),
      proc("ingest", "running", { uptimeSec: 720 }),
    ];
    draw();

    const t = await table();
    await waitFor(() => expect(processRows(t)).toHaveLength(2));
    const cells = (r: HTMLElement) => within(r).getAllByRole("cell").map((c) => c.textContent?.trim());
    const [failed, running] = processRows(t);
    // State, Restarts, Uptime, Errors. A failed process has no uptime to show.
    expect(cells(failed).slice(1)).toEqual(["3", "—", "1"]);
    expect(cells(running).slice(1)).toEqual(["0", "00:12:00", "1"]);
  });

  it("expanding a row reveals the reason, Show log and Show command", async () => {
    state.processes = [
      proc("dest:7", "failed", {
        restarts: 2,
        reason: { cause: "timeout", held: false, rejections: 0, at: new Date().toISOString() },
      }),
    ];
    draw();
    await table();

    const toggle = toggleFor("dest:7");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText(en("reason.cause.timeout"))).toBeNull();

    fireEvent.click(toggle);

    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    const detail = document.getElementById(toggle.getAttribute("aria-controls")!)!;
    expect(within(detail).getByText(en("reason.cause.timeout"))).toBeTruthy();
    expect(within(detail).getByRole("button", { name: en("reason.action.log") })).toBeTruthy();
    expect(within(detail).getByRole("button", { name: en("procTable.showCommand") })).toBeTruthy();

    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText(en("reason.cause.timeout"))).toBeNull();
  });

  it("keeps a row open across a re-sort, because it is keyed by name", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      state.processes = [proc("ingest", "running"), proc("dest:7", "running")];
      draw();
      await table();
      fireEvent.click(toggleFor("ingest"));
      expect(toggleFor("ingest").getAttribute("aria-expanded")).toBe("true");

      // The next poll finds dest:7 failed, so it moves to the top. The open
      // row must follow ingest, not stay on the first position.
      state.processes = [proc("ingest", "running"), proc("dest:7", "failed")];
      await act(() => vi.advanceTimersByTimeAsync(5000));
      const t = await table();
      await waitFor(() =>
        expect(processRows(t).map((r) => within(r).getByRole("rowheader").textContent?.trim())).toEqual([
          "dest:7",
          "ingest",
        ]),
      );
      expect(toggleFor("ingest").getAttribute("aria-expanded")).toBe("true");
      expect(toggleFor("dest:7").getAttribute("aria-expanded")).toBe("false");
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("Monitoring processes: everything the card held is reachable from an expanded row (A72)", () => {
  it("a classified failure: cause, remedy, Show log (which filters the log), PID, retry and the command", async () => {
    state.processes = [
      proc("dest:7", "failed", {
        pid: 4242,
        restarts: 2,
        nextRetryIn: 8,
        reason: { cause: "key_rejected", held: true, rejections: 3, at: new Date().toISOString() },
      }),
      proc("ingest", "running"),
    ];
    draw();
    await table();
    // Errors-only log shows both processes' errors before anything is chosen.
    expect(screen.getByText("ingest: connection reset")).toBeTruthy();

    fireEvent.click(toggleFor("dest:7"));
    const detail = document.getElementById(toggleFor("dest:7").getAttribute("aria-controls")!)!;
    const d = within(detail);

    expect(d.getByText(en("reason.cause.key_rejected"))).toBeTruthy();
    expect(d.getByText(en("reason.remedy.key_rejected"))).toBeTruthy();
    expect(d.getByText(new RegExp(String(4242)))).toBeTruthy();
    expect(d.getByText(en("i18nSweep.retryingIn", { seconds: "8" }))).toBeTruthy();
    // Exactly one Show log: the remedy's own and the row's are the same control.
    expect(d.getAllByRole("button", { name: en("reason.action.log") })).toHaveLength(1);

    const showCommand = d.getByRole("button", { name: en("procTable.showCommand") });
    expect(showCommand.getAttribute("aria-expanded")).toBe("false");
    expect(d.queryByText(/ffmpeg -i rtmp:\/\/relay\/dest:7/)).toBeNull();
    fireEvent.click(showCommand);
    expect(showCommand.getAttribute("aria-expanded")).toBe("true");
    expect(d.getByText(/ffmpeg -i rtmp:\/\/relay\/dest:7/)).toBeTruthy();

    // Show log lands on that process's own lines.
    fireEvent.click(d.getByRole("button", { name: en("reason.action.log") }));
    expect(screen.getByText("dest7: broken pipe")).toBeTruthy();
    expect(screen.queryByText("ingest: connection reset")).toBeNull();
  });

  it("a cause fixed in renditions keeps its link to /renditions, and still offers the log", async () => {
    state.processes = [
      proc("rendition:720p", "failed", {
        reason: { cause: "codec_rejected", held: false, rejections: 0, at: new Date().toISOString() },
      }),
    ];
    draw();
    await table();
    fireEvent.click(toggleFor("rendition:720p"));
    const d = within(document.getElementById(toggleFor("rendition:720p").getAttribute("aria-controls")!)!);

    expect(d.getByText(en("reason.remedy.codec_rejected"))).toBeTruthy();
    expect(d.getByRole("link", { name: en("reason.action.renditions") }).getAttribute("href")).toBe("/renditions");
    expect(d.getByRole("button", { name: en("reason.action.log") })).toBeTruthy();
  });

  it("an unclassified failure shows its raw last error", async () => {
    state.processes = [proc("preview", "failed", { lastError: "exit status 1: something odd" })];
    draw();
    await table();
    fireEvent.click(toggleFor("preview"));
    const d = within(document.getElementById(toggleFor("preview").getAttribute("aria-controls")!)!);
    expect(d.getByText("exit status 1: something odd")).toBeTruthy();
    expect(d.getByRole("button", { name: en("reason.action.log") })).toBeTruthy();
  });

  it("a healthy row still opens to its PID, Show log and command", async () => {
    state.processes = [proc("ingest", "running", { pid: 77, uptimeSec: 60 })];
    draw();
    await table();
    fireEvent.click(toggleFor("ingest"));
    const d = within(document.getElementById(toggleFor("ingest").getAttribute("aria-controls")!)!);
    expect(d.getByText(/77/)).toBeTruthy();
    expect(d.getByRole("button", { name: en("reason.action.log") })).toBeTruthy();
    fireEvent.click(d.getByRole("button", { name: en("procTable.showCommand") }));
    expect(d.getByText(/ffmpeg -i rtmp:\/\/relay\/ingest/)).toBeTruthy();
  });
});
