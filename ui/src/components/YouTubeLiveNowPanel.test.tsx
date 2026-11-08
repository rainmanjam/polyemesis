// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { YouTubeLiveNowPanel } from "./YouTubeLiveNowPanel";

const api = vi.hoisted(() => ({ youtubeStreams: vi.fn() }));
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
    const select = (await screen.findByRole("combobox")) as HTMLSelectElement;
    await screen.findByRole("option", { name: /Second/ });
    expect(select.value).toBe("");
    expect(onChange).not.toHaveBeenCalled();
  });

  it("stores the picked stream id and warns when another destination uses it", async () => {
    api.youtubeStreams.mockResolvedValue({ supported: true, streams: rows });
    const onChange = vi.fn();
    const { rerender } = render(<YouTubeLiveNowPanel accountId={3} controls={null} onChange={onChange} />);
    const select = await screen.findByRole("combobox");
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
    const select = await screen.findByRole("combobox");
    await screen.findByRole("option", { name: /Second/ });
    fireEvent.change(select, { target: { value: "" } });
    expect(onChange).toHaveBeenCalledWith({ streamId: undefined });
  });
});
