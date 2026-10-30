// @vitest-environment jsdom
//
// A2: the waiting state, and the way out of it. After about a minute with no
// frame, the panel links the troubleshooting section for an ingest that never
// goes live, rather than pulsing on for ever.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";

import { WaitingForEncoder, TROUBLESHOOT_INGEST_URL } from "./WaitingForEncoder";

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  vi.useRealTimers();
  cleanup();
});

describe("WaitingForEncoder", () => {
  it("says it is waiting, with no troubleshooting link at first", () => {
    render(<WaitingForEncoder />);
    expect(screen.getByText("Waiting for encoder")).toBeTruthy();
    expect(screen.queryByRole("link")).toBeNull();
  });

  it("links the troubleshooting section after a minute of silence", () => {
    render(<WaitingForEncoder />);
    act(() => vi.advanceTimersByTime(59_000));
    expect(screen.queryByRole("link")).toBeNull();
    act(() => vi.advanceTimersByTime(2_000));
    const link = screen.getByRole("link", { name: /Troubleshooting/ });
    expect(link.getAttribute("href")).toBe(TROUBLESHOOT_INGEST_URL);
  });
});
