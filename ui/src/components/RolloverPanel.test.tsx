// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";

import { RolloverPanel } from "./RolloverPanel";
import type { DestStatus, RolloverView } from "@/lib/types";

/* #19 on the destination card. The countdown is shown only when the platform's cap is verified, the destination
 * is delivering, and a cutoff is known. The reported status is said out loud, and an unverified cap shows no
 * number at all. */

const mocks = vi.hoisted(() => ({
  getDestinationSourceId: vi.fn(),
  getRollover: vi.fn(),
}));

vi.mock("@/lib/recurringApi", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/recurringApi")>();
  return { ...real, getDestinationSourceId: mocks.getDestinationSourceId, getRollover: mocks.getRollover };
});

afterEach(() => {
  cleanup();
  mocks.getDestinationSourceId.mockReset();
  mocks.getRollover.mockReset();
});

const dest = { id: 7, name: "Twitch main", platform: "twitch", enabled: true } as unknown as DestStatus;

function view(over: Partial<RolloverView>): RolloverView {
  return {
    settings: { destinationId: 7, enabled: false, marginMinutes: 30, chatNotice: false },
    platform: "twitch",
    action: "countdown",
    limit: {
      presetId: "twitch",
      kind: "broadcastDuration",
      hours: 48,
      status: "reported",
      source: "https://help.twitch.tv/s/article/broadcasting-guidelines?language=en_US",
    },
    delivering: true,
    due: false,
    ...over,
  };
}

describe("RolloverPanel countdown", () => {
  it("counts down to a verified cutoff and labels the cap as reported", async () => {
    mocks.getDestinationSourceId.mockResolvedValue(3);
    const cutoff = new Date(Date.now() + 2 * 3_600_000).toISOString();
    mocks.getRollover.mockResolvedValue(view({ cutoffAt: cutoff }));

    render(<RolloverPanel dest={dest} />);

    await waitFor(() => expect(screen.getByText(/until the platform's limit/)).toBeTruthy());
    expect(screen.getByText(/^[12]h \d\dm until the platform's limit$/)).toBeTruthy();
    expect(screen.getByText(/Reported by a secondary source/)).toBeTruthy();
    expect(mocks.getRollover).toHaveBeenCalledWith(7, 3);
  });

  it("waits for the platform's start when delivering with no cutoff yet", async () => {
    mocks.getDestinationSourceId.mockResolvedValue(null);
    mocks.getRollover.mockResolvedValue(view({ cutoffAt: undefined }));

    render(<RolloverPanel dest={dest} />);

    await waitFor(() => expect(screen.getByText(/Waiting for the platform/)).toBeTruthy());
  });

  it("shows no number for an unverified cap, only the reason", async () => {
    mocks.getDestinationSourceId.mockResolvedValue(3);
    mocks.getRollover.mockResolvedValue(
      view({
        cutoffAt: new Date(Date.now() + 3_600_000).toISOString(),
        limit: { presetId: "kick", kind: "broadcastDuration", hours: 0, status: "unverified" },
      }),
    );

    render(<RolloverPanel dest={dest} />);

    await waitFor(() => expect(screen.getByText(/not verified yet/)).toBeTruthy());
    expect(screen.queryByText(/until the platform's limit/)).toBeNull();
  });

  it("shows no countdown while the destination is not delivering", async () => {
    mocks.getDestinationSourceId.mockResolvedValue(3);
    mocks.getRollover.mockResolvedValue(view({ delivering: false }));

    render(<RolloverPanel dest={dest} />);

    await waitFor(() => expect(screen.getByText(/not delivering right now/)).toBeTruthy());
  });

  it("renders nothing for a platform with no cap", async () => {
    mocks.getDestinationSourceId.mockResolvedValue(3);
    mocks.getRollover.mockResolvedValue(view({ limit: undefined }));

    const { container } = render(<RolloverPanel dest={dest} />);

    await waitFor(() => expect(mocks.getRollover).toHaveBeenCalled());
    expect(container.textContent).toBe("");
  });
});
