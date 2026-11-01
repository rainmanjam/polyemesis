// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { YouTubeVerticalPanel } from "./YouTubeVerticalPanel";
import { ApiError } from "@/lib/api";
import type { DestinationId, YouTubeVerticalView } from "@/lib/types";

// The pairing panel in the destination dialog (#7). Adding the half and confirming the
// Live Control Room step are separate API calls, and the panel must make exactly the
// one the operator asked for.

const api = vi.hoisted(() => ({
  youtubeVerticalStatus: vi.fn(),
  createYouTubeVertical: vi.fn(async () => ({ horizontalId: 1, verticalId: 2, verticalStreamId: "s" })),
  confirmYouTubeVertical: vi.fn(async () => ({ pairingConfirmed: true })),
}));

vi.mock("@/lib/api", () => ({
  api,
  ApiError: class extends Error {
    status: number;
    constructor(message: string, status: number) {
      super(message);
      this.status = status;
    }
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const id = 1 as DestinationId;

describe("the YouTube vertical panel", () => {
  it("offers a tier and adds the half for the one the operator picked", async () => {
    api.youtubeVerticalStatus
      .mockRejectedValueOnce(new (ApiError as unknown as new (m: string, s: number) => Error)("none", 404))
      .mockResolvedValueOnce({
        horizontalId: id, verticalId: 2 as DestinationId, verticalStreamId: "s",
        pairingConfirmed: false, live: false,
      } satisfies YouTubeVerticalView);
    render(<YouTubeVerticalPanel destinationId={id} />);
    const add = await screen.findByRole("button", { name: /Add vertical half/ });
    fireEvent.click(add);
    await waitFor(() =>
      expect(api.createYouTubeVertical).toHaveBeenCalledWith(id, "vertical1080p30"),
    );
  });

  it("shows the one-time steps until the operator confirms, then confirms exactly once", async () => {
    const unconfirmed: YouTubeVerticalView = {
      horizontalId: id, verticalId: 2 as DestinationId, verticalStreamId: "s",
      pairingConfirmed: false, live: false,
    };
    api.youtubeVerticalStatus.mockResolvedValue(unconfirmed);
    render(<YouTubeVerticalPanel destinationId={id} />);
    expect(await screen.findByText(/Choose the second stream key|second stream key/i)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /I have done this step/ }));
    await waitFor(() => expect(api.confirmYouTubeVertical).toHaveBeenCalledTimes(1));
    expect(api.confirmYouTubeVertical).toHaveBeenCalledWith(id);
  });

  // Only a 404 means "no vertical half yet". Any other failure is a failed read, and the
  // pair may well exist: offering "Add" there would send a create the server refuses with
  // a 409. The panel says the read failed and offers a retry instead.
  it("shows a failed status read as an error with a retry, not as an unpaired destination", async () => {
    const Err = ApiError as unknown as new (m: string, s: number) => Error;
    api.youtubeVerticalStatus
      .mockRejectedValueOnce(new Err("the store is unavailable", 500))
      .mockRejectedValueOnce(new Err("none", 404));
    render(<YouTubeVerticalPanel destinationId={id} />);
    expect(await screen.findByText(/the store is unavailable/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Add vertical half/ })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("button", { name: /Add vertical half/ })).toBeTruthy();
    expect(api.youtubeVerticalStatus).toHaveBeenCalledTimes(2);
    expect(screen.queryByText(/the store is unavailable/)).toBeNull();
  });

  it("says the pairing is provisional and that polyemesis does not check it", async () => {
    api.youtubeVerticalStatus.mockResolvedValue({
      horizontalId: id, verticalId: 2 as DestinationId, verticalStreamId: "s",
      pairingConfirmed: false, live: false,
    } satisfies YouTubeVerticalView);
    render(<YouTubeVerticalPanel destinationId={id} />);
    expect(await screen.findByText(/PROVISIONAL/)).toBeTruthy();
  });
});
