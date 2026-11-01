// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { YouTubeVerticalSubRow } from "./YouTubeVerticalSubRow";
import type { DestStatus, DestinationId, YouTubeVerticalView } from "@/lib/types";

// The sub-row is where a failed vertical half shows up (#7). Its own state, its own
// controls and its own health line: a failure here must read as this half's failure, and
// the horizontal card's controls must not be the ones that act on it.

const status = vi.hoisted(() => ({
  view: null as YouTubeVerticalView | null,
}));

vi.mock("@/lib/api", () => ({
  api: {
    youtubeVerticalStatus: vi.fn(async () => {
      if (status.view) return status.view;
      throw new Error("no pair");
    }),
  },
  ApiError: class extends Error {},
}));

afterEach(() => {
  cleanup();
  status.view = null;
});

const horizontal = (over: Partial<DestStatus> = {}): DestStatus =>
  ({ id: 1, name: "YouTube main", kind: "rtmp", platform: "youtube", enabled: true,
     process: { state: "running" }, ...over }) as unknown as DestStatus;

const vertical = (over: Partial<DestStatus> = {}): DestStatus =>
  ({ id: 2, name: "YouTube main (vertical)", kind: "rtmp", platform: "youtube", enabled: true,
     verticalHalfOf: 1, process: { state: "stopped" }, ...over }) as unknown as DestStatus;

describe("the vertical sub-row", () => {
  it("shows a failed vertical half on its own row, with its error", () => {
    render(
      <YouTubeVerticalSubRow
        horizontal={horizontal()}
        vertical={vertical({ error: "the vertical stream was rejected" })}
        onStart={() => {}}
        onStop={() => {}}
        onRestart={() => {}}
      />,
    );
    expect(screen.getByText("the vertical stream was rejected")).toBeTruthy();
  });

  it("starts only the vertical half when its Start is pressed", () => {
    const onStart = vi.fn();
    const onStop = vi.fn();
    render(
      <YouTubeVerticalSubRow
        horizontal={horizontal()}
        vertical={vertical()}
        onStart={onStart}
        onStop={onStop}
        onRestart={() => {}}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Start" }));
    expect(onStart).toHaveBeenCalledTimes(1);
    expect(onStop).not.toHaveBeenCalled();
  });

  // A pair created while the horizontal is disabled has a disabled vertical half, and the
  // vertical's own card is never drawn, so this row is the only Start it has. Start is the
  // enable path on the server (handleStartDestination enables before it starts), so it must
  // stay pressable on a disabled half; a disabled Start here would strand the half forever.
  it("lets a disabled vertical half be started from its row, because Start is how it is enabled", () => {
    const onStart = vi.fn();
    render(
      <YouTubeVerticalSubRow
        horizontal={horizontal({ enabled: false, process: { state: "stopped" } as DestStatus["process"] })}
        vertical={vertical({ enabled: false })}
        onStart={onStart}
        onStop={() => {}}
        onRestart={() => {}}
      />,
    );
    const start = screen.getByRole("button", { name: "Start" }) as HTMLButtonElement;
    expect(start.disabled).toBe(false);
    fireEvent.click(start);
    expect(onStart).toHaveBeenCalledTimes(1);
  });

  it("shows YouTube's health reading and a quota pause on the sub-row", async () => {
    status.view = {
      horizontalId: 1 as DestinationId, verticalId: 2 as DestinationId, verticalStreamId: "s", pairingConfirmed: true, live: true,
      healthError: "the YouTube quota is nearly spent; the vertical status is paused until it resets",
    };
    render(
      <YouTubeVerticalSubRow
        horizontal={horizontal()}
        vertical={vertical({ process: { state: "running" } as DestStatus["process"] })}
        onStart={() => {}}
        onStop={() => {}}
        onRestart={() => {}}
      />,
    );
    await waitFor(() => expect(screen.getByText(/quota is nearly spent/)).toBeTruthy());
  });
});
