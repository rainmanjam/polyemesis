// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { translate } from "@/lib/i18n";

/* A FAILED REFRESH DID NOT ERASE A TIMELINE, IT HID IT. (A69)
 *
 * The timeline re-reads every 15 seconds. One failed read replaced every loaded
 * incident with "Could not load the timeline", so an operator looking at the
 * incident of five minutes ago lost it to a network blip, with no time to say
 * how old the list on screen was. A failed refresh now keeps the rows that were
 * last read and says when they were read.
 *
 * The first load still shows the plain failure, because there is nothing to keep.
 *
 * Plain functions for the API, not a rejecting vi.fn(): see ClipsPage.readState. */

const replies: Array<"ok" | "fail"> = [];
const rows = [
  { id: 2, at: "2026-10-29T20:05:00Z", type: "destination.down", severity: "warning", title: "Destination down: Twitch" },
  { id: 1, at: "2026-10-29T20:00:00Z", type: "failover.switched", severity: "critical", title: "Failover: primary lost" },
];

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      listTimeline: () => {
        const next = replies.shift() ?? "ok";
        return next === "fail" ? Promise.reject(new Error("offline")) : Promise.resolve(rows);
      },
    },
  };
});

const { MonitorTimeline } = await import("./MonitorTimeline");

afterEach(() => {
  cleanup();
  replies.length = 0;
  vi.useRealTimers();
});

describe("MonitorTimeline: a failed refresh keeps what it had (A69)", () => {
  it("keeps the loaded rows on screen and says the list is from an earlier read", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    replies.push("ok", "fail");
    render(<MemoryRouter><MonitorTimeline /></MemoryRouter>);
    expect(await screen.findByText("Failover: primary lost")).toBeTruthy();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(15_000);
    });

    expect(
      screen.getByText("Failover: primary lost"),
      "a failed refresh must not hide the incident already on screen",
    ).toBeTruthy();
    const alert = screen.getByRole("alert");
    expect(alert.textContent).toContain("Could not refresh");
  });

  it("shows the plain failure when there was never a list to keep", async () => {
    replies.push("fail");
    render(<MemoryRouter><MonitorTimeline /></MemoryRouter>);

    expect(await screen.findByText(translate("en", "timeline.loadFailed"))).toBeTruthy();
  });
});
