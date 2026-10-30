// @vitest-environment jsdom

import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

import { PollPanel } from "./PollPanel";
import { api } from "@/lib/api";
import type { Poll, PollsResponse } from "@/lib/polls";

/* The poll panel on the Chat page (#14): start a poll, watch it with its
 * per-platform breakdown, end it, export it. Written against what the operator
 * can reach and what is then sent to the server. */

const list = vi.spyOn(api, "listPolls");
const start = vi.spyOn(api, "startPoll");
const end = vi.spyOn(api, "endPoll");

const running: Poll = {
  id: 7,
  question: "Next map?",
  options: [
    { label: "Dust", votes: 3, byPlatform: { twitch: 2, youtube: 1 } },
    { label: "Mirage", votes: 1, byPlatform: { kick: 1 } },
  ],
  total: 4,
  byPlatform: { twitch: 2, youtube: 1, kick: 1 },
  state: "open",
  durationSeconds: 120,
  startedAt: new Date().toISOString(),
  endsAt: new Date(Date.now() + 65_000).toISOString(),
  announced: [
    { platform: "twitch", ok: true },
    { platform: "youtube", ok: false, detail: "quota exceeded" },
  ],
  native: [{ platform: "twitch", status: "skipped", reason: "not an Affiliate" }],
};

const finished: Poll = {
  ...running, id: 6, question: "Snack?", state: "ended", endedAt: new Date().toISOString(), announced: [], native: [],
};

const respond = (r: PollsResponse) => list.mockResolvedValue(r);

beforeAll(() => {
  // Radix's Switch measures itself; jsdom has no ResizeObserver.
  (window as unknown as { ResizeObserver?: unknown }).ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
});

beforeEach(() => {
  list.mockReset();
  start.mockReset();
  end.mockReset();
});
afterEach(cleanup);

describe("starting a poll", () => {
  beforeEach(() => respond({ current: null, history: [] }));

  it("says what is wrong instead of sending a poll the server would refuse", async () => {
    render(<PollPanel />);
    fireEvent.click(await screen.findByRole("button", { name: /Start poll/ }));
    expect((await screen.findByRole("alert")).textContent).toContain("Type a question.");
    fireEvent.change(screen.getByLabelText("Question"), { target: { value: "Next map?" } });
    fireEvent.change(screen.getByLabelText("Option 1"), { target: { value: "Dust" } });
    fireEvent.change(screen.getByLabelText("Option 2"), { target: { value: "dust" } });
    fireEvent.click(screen.getByRole("button", { name: /Start poll/ }));
    expect((await screen.findByRole("alert")).textContent).toContain("Two options are the same.");
    expect(start).not.toHaveBeenCalled();
  });

  it("sends the question, the options, the duration and the Twitch mirror choice", async () => {
    start.mockResolvedValue(running);
    render(<PollPanel />);
    fireEvent.change(await screen.findByLabelText("Question"), { target: { value: "Next map?" } });
    fireEvent.change(screen.getByLabelText("Option 1"), { target: { value: "Dust" } });
    fireEvent.change(screen.getByLabelText("Option 2"), { target: { value: "Mirage" } });
    fireEvent.click(screen.getByRole("button", { name: /Add an option/ }));
    fireEvent.change(screen.getByLabelText("Option 3"), { target: { value: "Nuke" } });
    fireEvent.click(screen.getByRole("button", { name: "Remove option 3" }));
    fireEvent.change(screen.getByLabelText("Duration"), { target: { value: "45" } });
    fireEvent.click(screen.getByLabelText("Also run as a native Twitch poll"));
    fireEvent.click(screen.getByRole("button", { name: /Start poll/ }));
    await waitFor(() => expect(start).toHaveBeenCalled());
    expect(start.mock.calls[0][0]).toEqual({
      question: "Next map?", options: ["Dust", "Mirage"], durationSeconds: 45, mirrorTwitch: false,
    });
  });

  it("offers no more than six options and no fewer than two", async () => {
    render(<PollPanel />);
    await screen.findByLabelText("Option 1");
    expect(screen.queryByRole("button", { name: /Remove option/ })).toBeNull();
    for (let i = 0; i < 4; i++) fireEvent.click(screen.getByRole("button", { name: /Add an option/ }));
    expect(screen.getByLabelText("Option 6")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Add an option/ })).toBeNull();
  });
});

describe("a running poll", () => {
  it("shows each option's count with the platform breakdown, the total and the time left", async () => {
    respond({ current: running, history: [running, finished] });
    render(<PollPanel />);
    expect(await screen.findByText("Next map?")).toBeTruthy();
    expect(screen.getByText("3 votes · 75%")).toBeTruthy();
    expect(screen.getByText("1 votes · 25%")).toBeTruthy();
    const total = screen.getByTestId("poll-total");
    expect(total.textContent).toContain("4 votes in all");
    expect(total.textContent).toContain("Twitch 2");
    expect(screen.getByTestId("poll-state").textContent).toMatch(/1:0[0-5] left/);
    const delivery = screen.getByTestId("poll-delivery");
    expect(within(delivery).getByText("Announced on Twitch")).toBeTruthy();
    expect(within(delivery).getByText(/quota exceeded/)).toBeTruthy();
    expect(within(delivery).getByText(/not an Affiliate/)).toBeTruthy();
    // The running poll is not repeated in the history; the finished one is.
    expect(screen.getByText("Snack?")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Start poll/ })).toBeNull();
  });

  it("ends it with End now", async () => {
    respond({ current: running, history: [] });
    end.mockResolvedValue({ ...running, state: "ended" });
    render(<PollPanel />);
    fireEvent.click(await screen.findByRole("button", { name: /End now/ }));
    await waitFor(() => expect(end).toHaveBeenCalledWith(7));
  });

  it("cannot be ended twice while it is counting the last votes", async () => {
    respond({ current: { ...running, state: "closing" }, history: [] });
    render(<PollPanel />);
    expect(await screen.findByText("Counting the last votes…")).toBeTruthy();
    expect((screen.getByRole("button", { name: /End now/ }) as HTMLButtonElement).disabled).toBe(true);
  });
});

describe("history and export", () => {
  it("links each finished poll's CSV and the whole history's", async () => {
    respond({ current: null, history: [finished, { ...finished, id: 5, question: "Lost", state: "interrupted" }] });
    render(<PollPanel />);
    const all = await screen.findByRole("link", { name: /Export all/ });
    expect(all.getAttribute("href")).toBe("/api/v1/polls/export");
    expect(screen.getByRole("link", { name: "Export the poll “Snack?” as CSV" }).getAttribute("href"))
      .toBe("/api/v1/polls/export?id=6");
    expect(screen.getByText("Dust won · 4 votes")).toBeTruthy();
    expect(screen.getByText(/Interrupted/)).toBeTruthy();
  });

  it("says polls are unavailable when the server has no chat", async () => {
    list.mockRejectedValue(new Error("polls need the chat hub"));
    render(<PollPanel />);
    expect(await screen.findByText(/Polls need chat/)).toBeTruthy();
  });
});
