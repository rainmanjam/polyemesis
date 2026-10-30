// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

/* The timeline is the answer to "when did it fail over?" after the fact. It
 * shows the stream's transitions newest first, says so plainly when there are
 * none, and says so when it could not load them, rather than showing an empty
 * list that reads as "nothing happened". */

const listTimeline = vi.fn();

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { ...actual.api, listTimeline: (...a: unknown[]) => listTimeline(...a) } };
});

import { MonitorTimeline } from "./MonitorTimeline";

afterEach(() => {
  cleanup();
  listTimeline.mockReset();
});

describe("MonitorTimeline", () => {
  it("lists the stored transitions newest first with their titles", async () => {
    listTimeline.mockResolvedValue([
      { id: 2, at: "2026-10-29T20:05:00Z", type: "destination.down", severity: "warning", title: "Destination down: Twitch" },
      { id: 1, at: "2026-10-29T20:00:00Z", type: "failover.switched", severity: "critical", title: "Failover: primary lost" },
    ]);
    render(<MonitorTimeline />);
    const items = await screen.findAllByRole("listitem");
    expect(items.map((li) => li.textContent)).toEqual(
      expect.arrayContaining([expect.stringContaining("Destination down: Twitch"), expect.stringContaining("Failover: primary lost")]),
    );
    expect(items[0].textContent).toContain("Destination down: Twitch");
  });

  it("says plainly when nothing has been recorded", async () => {
    listTimeline.mockResolvedValue([]);
    render(<MonitorTimeline />);
    expect(await screen.findByText("No failovers, restarts or outages recorded yet.")).toBeTruthy();
  });

  it("says so when the history could not be loaded instead of showing an empty list", async () => {
    listTimeline.mockRejectedValue(new Error("boom"));
    render(<MonitorTimeline />);
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("Could not load the timeline."));
    expect(screen.queryByText("No failovers, restarts or outages recorded yet.")).toBeNull();
  });
  it("filters by event type and shows each row's detail (A13)", async () => {
    listTimeline.mockResolvedValue([
      { id: 3, at: "2026-10-29T20:06:00Z", type: "ingest.lost", severity: "critical", title: "Ingest lost" },
      { id: 2, at: "2026-10-29T20:05:00Z", type: "destination.down", severity: "warning", title: "Destination down: Twitch", detail: "2nd in 10 min" },
      { id: 1, at: "2026-10-29T20:00:00Z", type: "failover.switched", severity: "critical", title: "Failover: primary lost" },
    ]);
    render(<MonitorTimeline />);
    await screen.findAllByRole("listitem");
    expect(screen.getByText("2nd in 10 min")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Failover" }));
    expect(screen.getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      expect.stringContaining("Failover: primary lost"),
    ]);
    expect(screen.getByRole("button", { name: "Failover" }).getAttribute("aria-pressed")).toBe("true");

    fireEvent.click(screen.getByRole("button", { name: "Destinations" }));
    expect(screen.getAllByRole("listitem")).toHaveLength(1);

    fireEvent.click(screen.getByRole("button", { name: "All" }));
    expect(screen.getAllByRole("listitem")).toHaveLength(3);
  });

  it("offers only the filters the history has rows for", async () => {
    listTimeline.mockResolvedValue([
      { id: 1, at: "2026-10-29T20:00:00Z", type: "failover.switched", severity: "critical", title: "Failover" },
    ]);
    render(<MonitorTimeline />);
    await screen.findAllByRole("listitem");
    expect(screen.queryByRole("button", { name: "Destinations" })).toBeNull();
  });
});
