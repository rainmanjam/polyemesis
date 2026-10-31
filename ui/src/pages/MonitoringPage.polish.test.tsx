// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { readFileSync } from "node:fs";

/* MONITORING POLISH (app-council A93, A83).
 *
 * A93: the memory card printed "· rss" and the ingest chart's tooltip named its
 * series "ingest", English literals in a fifteen-locale console. Both now come
 * from the catalogue.
 *
 * A83: the Expert panel's "choose a destination" box and the log panel's two
 * empty messages were hand-drawn (a dashed box, a centred muted line). They are
 * the kit's EmptyState now, the one shape for "nothing here yet". */

const state = vi.hoisted(() => ({
  system: null as unknown,
}));

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({
    system: state.system,
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
      listProcesses: () => Promise.resolve([]),
      processLogs: () => Promise.resolve({ lines: [] }),
      listDestinations: () => Promise.resolve([]),
      listTimeline: () => Promise.resolve([]),
      jobsOverview: () => Promise.resolve({ policy: { cpuCeilingPercent: 85 } }),
    },
  };
});

const { ExpertPanel, MonitoringPage } = await import("./MonitoringPage");
const { setLanguage, translate } = await import("@/lib/i18n");

beforeAll(() => {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.releasePointerCapture = () => {};
  window.HTMLElement.prototype.setPointerCapture = () => {};
  window.HTMLElement.prototype.scrollIntoView = () => {};
});

afterEach(() => {
  cleanup();
  setLanguage("en");
  state.system = null;
});

describe("MonitoringPage: no English literals around the figures (app-council A93)", () => {
  it("prints the memory line from the catalogue, translated", () => {
    state.system = { memPercent: 40, memUsedBytes: 4 * 1024 ** 3, memTotalBytes: 16 * 1024 ** 3, procMemBytes: 512 * 1024 ** 2 };
    setLanguage("de");
    const { container } = render(<MemoryRouter><MonitoringPage /></MemoryRouter>);
    expect(container.textContent).toContain("· Prozess ");
    expect(container.textContent).not.toMatch(/· rss/);
  });

  it("names the chart series from the catalogue rather than a literal", () => {
    // The tooltip only renders on hover in a real layout, which jsdom does not
    // do, so the source is the evidence: no quoted series name is left.
    const src = readFileSync("src/pages/MonitoringPage.tsx", "utf8");
    expect(src).not.toMatch(/\[kbps\(v as number\), "ingest"\]/);
    expect(src).not.toMatch(/· rss/);
  });
});

describe("MonitoringPage: empty lists use the kit's EmptyState (app-council A83)", () => {
  it("shows the log panel's empty message as an EmptyState, not a bare line", () => {
    render(<MemoryRouter><MonitoringPage /></MemoryRouter>);
    const title = screen.getByText(translate("en", "mon.noLogLinesTitle"));
    expect(title.className).toContain("font-semibold");
    expect(title.closest(".border-dashed")).not.toBeNull();
  });

  it("asks for a destination in the Expert panel with an EmptyState, not a hand-drawn dashed box", async () => {
    render(<MemoryRouter><ExpertPanel /></MemoryRouter>);
    const title = await screen.findByText(translate("en", "mon.chooseDestinationToEdit"));
    // EmptyState sets its title as a semibold paragraph; the old box was a div of tiny text.
    expect(title.tagName).toBe("P");
    expect(title.className).toContain("font-semibold");
  });
});
