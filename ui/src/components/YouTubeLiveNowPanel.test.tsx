// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { YouTubeLiveNowPanel } from "./YouTubeLiveNowPanel";

const api = vi.hoisted(() => ({
  youtubeStreams: vi.fn(),
  youtubeEvents: vi.fn(),
  youtubeEventAutoStart: vi.fn(),
}));
vi.mock("@/lib/api", () => ({ api, ApiError: class extends Error {} }));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const rows = [
  { id: "s-a", title: "Main", streamStatus: "active", health: "good", current: true },
  { id: "s-b", title: "Second", streamStatus: "inactive", health: "noData", usedBy: [{ id: 7, name: "Backup show" }] },
];

describe("the YouTube stream picker", () => {
  it("defaults to the stream matching the current key and stores nothing", async () => {
    api.youtubeStreams.mockResolvedValue({ supported: true, streams: rows });
    const onChange = vi.fn();
    render(<YouTubeLiveNowPanel accountId={3} controls={null} onChange={onChange} />);
    const select = (await screen.findByLabelText(/YouTube stream/)) as HTMLSelectElement;
    await screen.findByRole("option", { name: /Second/ });
    expect(select.value).toBe("");
    expect(onChange).not.toHaveBeenCalled();
  });

  it("stores the picked stream id and warns when another destination uses it", async () => {
    api.youtubeStreams.mockResolvedValue({ supported: true, streams: rows });
    const onChange = vi.fn();
    const { rerender } = render(<YouTubeLiveNowPanel accountId={3} controls={null} onChange={onChange} />);
    const select = await screen.findByLabelText(/YouTube stream/);
    await screen.findByRole("option", { name: /Second/ });
    fireEvent.change(select, { target: { value: "s-b" } });
    expect(onChange).toHaveBeenCalledWith({ streamId: "s-b" });
    rerender(<YouTubeLiveNowPanel accountId={3} controls={{ streamId: "s-b" }} onChange={onChange} />);
    expect(await screen.findByText(/Backup show/)).toBeTruthy();
  });

  it("clears the pin when the default is chosen again", async () => {
    api.youtubeStreams.mockResolvedValue({ supported: true, streams: rows });
    const onChange = vi.fn();
    render(<YouTubeLiveNowPanel accountId={3} controls={{ streamId: "s-b" }} onChange={onChange} />);
    const select = await screen.findByLabelText(/YouTube stream/);
    await screen.findByRole("option", { name: /Second/ });
    fireEvent.change(select, { target: { value: "" } });
    expect(onChange).toHaveBeenCalledWith({ streamId: undefined });
  });
});

describe("the Studio Stream now switch", () => {
  it("is off by default, and turning it on stores the flag and hides the stream picker", async () => {
    api.youtubeStreams.mockResolvedValue({ supported: true, streams: rows });
    const onChange = vi.fn();
    const { rerender } = render(<YouTubeLiveNowPanel accountId={3} controls={null} onChange={onChange} />);
    const sw = screen.getByLabelText(/Stream now instead/);
    expect(sw.getAttribute("aria-checked")).toBe("false");
    expect(screen.getByLabelText(/YouTube stream/)).toBeTruthy();
    fireEvent.click(sw);
    expect(onChange).toHaveBeenCalledWith({ studioStreamNow: true });
    rerender(<YouTubeLiveNowPanel accountId={3} controls={{ studioStreamNow: true }} onChange={onChange} />);
    expect(screen.queryByLabelText(/YouTube stream/)).toBeNull();
    fireEvent.click(screen.getByLabelText(/Stream now instead/));
    expect(onChange).toHaveBeenLastCalledWith({ studioStreamNow: undefined });
  });
});

const ev = (over: Record<string, unknown>) => ({
  id: "ev-1", title: "Friday", status: "ready", enableAutoStart: true, enableAutoStop: true,
  boundStreamId: "s-a", streamActive: false, onThisStream: true, ...over,
});

describe("picking an existing event", () => {
  const pick = async (events: unknown[], controls: object = { eventId: "ev-1" }, onChange = vi.fn()) => {
    api.youtubeStreams.mockResolvedValue({ supported: true, streams: [] });
    api.youtubeEvents.mockResolvedValue({ supported: true, events });
    render(<YouTubeLiveNowPanel accountId={3} controls={controls} onChange={onChange} />);
    await screen.findByRole("option", { name: /Friday/ });
    return onChange;
  };

  it("lists events only after the existing-event mode is chosen, and stores the picked id", async () => {
    api.youtubeStreams.mockResolvedValue({ supported: true, streams: [] });
    api.youtubeEvents.mockResolvedValue({ supported: true, events: [ev({})] });
    const onChange = vi.fn();
    render(<YouTubeLiveNowPanel accountId={3} controls={null} onChange={onChange} />);
    expect(api.youtubeEvents).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText(/When I go live/), { target: { value: "existing" } });
    const select = await screen.findByLabelText(/YouTube event/);
    await screen.findByRole("option", { name: /Friday/ });
    fireEvent.change(select, { target: { value: "ev-1" } });
    expect(onChange).toHaveBeenCalledWith({ eventId: "ev-1", rebindEvent: undefined });
  });

  it("warns that an auto-start-off event will not go live by itself, and the button is the only thing that changes it", async () => {
    api.youtubeEventAutoStart.mockResolvedValue({ eventId: "ev-1", enableAutoStart: true });
    await pick([ev({ enableAutoStart: false })]);
    expect(await screen.findByText(/won't go live by itself/)).toBeTruthy();
    expect(api.youtubeEventAutoStart).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: /Turn auto-start on/ }));
    await waitFor(() => expect(api.youtubeEventAutoStart).toHaveBeenCalledWith(3, "ev-1"));
  });

  it("says polyemesis ends an event whose auto-stop is off", async () => {
    await pick([ev({ enableAutoStop: false })]);
    expect(await screen.findByText(/polyemesis ends it/)).toBeTruthy();
  });

  it("never offers to move an event off an active stream, and asks before moving one off an idle stream", async () => {
    const onChange = await pick([ev({ onThisStream: false, streamActive: true })]);
    expect(await screen.findByText(/will not move it/)).toBeTruthy();
    expect(screen.queryByRole("switch", { name: /Move this event/ })).toBeNull();
    cleanup();
    const onChange2 = await pick([ev({ onThisStream: false, streamActive: false })], { eventId: "ev-1" }, onChange);
    fireEvent.click(await screen.findByRole("switch", { name: /Move this event/ }));
    expect(onChange2).toHaveBeenCalledWith({ rebindEvent: true });
  });

  it("clears the pick when going back to creating a broadcast", async () => {
    const onChange = await pick([ev({})]);
    fireEvent.change(screen.getByLabelText(/When I go live/), { target: { value: "create" } });
    expect(onChange).toHaveBeenCalledWith({ eventId: undefined, rebindEvent: undefined });
  });
});
