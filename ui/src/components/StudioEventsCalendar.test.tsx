// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { toast } from "sonner";

import { StudioEventsCalendar } from "./StudioEventsCalendar";

const api = vi.hoisted(() => ({
  youtubeStudioEvents: vi.fn(),
  listDestinations: vi.fn(),
  updateDestination: vi.fn(),
}));
vi.mock("@/lib/api", () => ({ api, ApiError: class extends Error {} }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const events = [
  { accountId: 3, accountName: "chan", id: "ev-1", title: "Studio show", scheduledStart: "2030-01-01T18:00:00Z", status: "ready",
    enableAutoStart: false, studioUrl: "https://studio.youtube.com/video/ev-1/livestreaming" },
  { accountId: 3, accountName: "chan", id: "ev-own", title: "Made by polyemesis", scheduledStart: "2030-01-02T18:00:00Z", status: "ready",
    enableAutoStart: true, studioUrl: "https://studio.youtube.com/video/ev-own/livestreaming" },
];
const dests = [
  { id: 7, name: "Main YT", platform: "youtube", accountId: 3, compliance: { youtube: { latencyPreference: "low" } } },
  { id: 8, name: "Other account", platform: "youtube", accountId: 4 },
  { id: 9, name: "Twitch", platform: "twitch", accountId: 3 },
];

describe("YouTube Studio events in the calendar", () => {
  it("shows Studio's events apart, links to Studio, and leaves out an event polyemesis made itself", async () => {
    api.youtubeStudioEvents.mockResolvedValue({ events, stale: [] });
    api.listDestinations.mockResolvedValue(dests.map((destination) => ({ destination })));
    render(<StudioEventsCalendar ownEventIds={new Set(["ev-own"])} />);
    expect(await screen.findByText("Studio show")).toBeTruthy();
    expect(screen.queryByText("Made by polyemesis")).toBeNull();
    expect(screen.getByText("from YouTube Studio")).toBeTruthy();
    expect(screen.getByRole("link", { name: /Open in Studio/ }).getAttribute("href")).toBe(events[0].studioUrl);
  });

  it("offers 'Use for' only for YouTube destinations on the event's own account, and stores the event id", async () => {
    api.youtubeStudioEvents.mockResolvedValue({ events: [events[0]], stale: [] });
    api.listDestinations.mockResolvedValue(dests.map((destination) => ({ destination })));
    api.updateDestination.mockResolvedValue({});
    render(<StudioEventsCalendar ownEventIds={new Set()} />);
    const btn = await screen.findByRole("button", { name: /Use for Main YT/ });
    expect(screen.queryByRole("button", { name: /Other account/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Twitch/ })).toBeNull();
    fireEvent.click(btn);
    await waitFor(() =>
      expect(api.updateDestination).toHaveBeenCalledWith(7, {
        compliance: { youtube: { latencyPreference: "low", eventId: "ev-1", rebindEvent: false } },
      }),
    );
  });

  it("does not carry over a stale rebind confirmation to the newly picked event", async () => {
    api.youtubeStudioEvents.mockResolvedValue({ events: [events[0]], stale: [] });
    api.listDestinations.mockResolvedValue([
      { destination: { ...dests[0], compliance: { youtube: { eventId: "old", rebindEvent: true } } } },
    ]);
    api.updateDestination.mockResolvedValue({});
    render(<StudioEventsCalendar ownEventIds={new Set()} />);
    fireEvent.click(await screen.findByRole("button", { name: /Use for Main YT/ }));
    await waitFor(() =>
      expect(api.updateDestination).toHaveBeenCalledWith(7, {
        compliance: { youtube: { eventId: "ev-1", rebindEvent: false } },
      }),
    );
  });

  it("blocks the pick, with an explanation, when the destination uses Studio's Stream now", async () => {
    api.youtubeStudioEvents.mockResolvedValue({ events: [events[0]], stale: [] });
    api.listDestinations.mockResolvedValue([
      { destination: { ...dests[0], compliance: { youtube: { studioStreamNow: true } } } },
    ]);
    render(<StudioEventsCalendar ownEventIds={new Set()} />);
    fireEvent.click(await screen.findByRole("button", { name: /Use for Main YT/ }));
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(expect.stringMatching(/Stream now/)));
    expect(api.updateDestination).not.toHaveBeenCalled();
  });

  it("renders nothing when the read fails, so the calendar's own rows are unaffected", async () => {
    api.youtubeStudioEvents.mockRejectedValue(new Error("down"));
    api.listDestinations.mockResolvedValue([]);
    const { container } = render(<StudioEventsCalendar ownEventIds={new Set()} />);
    await waitFor(() => expect(api.youtubeStudioEvents).toHaveBeenCalled());
    expect(container.querySelector("[data-testid=studio-events]")).toBeNull();
  });
});
