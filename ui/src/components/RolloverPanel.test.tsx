// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";

import { ApiError } from "@/lib/api";

import { RolloverPanel } from "./RolloverPanel";
import type { DestStatus, RolloverView } from "@/lib/types";

/* #19 on the destination card. The countdown is shown only when the platform's cap is verified, the destination
 * is delivering, and a cutoff is known. The reported status is said out loud, and an unverified cap shows no
 * number at all. */

const mocks = vi.hoisted(() => ({
  getRollover: vi.fn(),
}));

vi.mock("@/lib/recurringApi", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/recurringApi")>();
  return { ...real, getRollover: mocks.getRollover };
});

afterEach(() => {
  cleanup();
  mocks.getRollover.mockReset();
  vi.unstubAllGlobals();
});

const dest = { id: 7, name: "Twitch main", platform: "twitch", enabled: true, sourceId: 3 } as unknown as DestStatus;

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

describe("RolloverPanel's read", () => {
  /* ONE READ, AT MOUNT, NAMING THE PROGRAMME THE CARD ALREADY HAS.
   * The panel used to resolve the programme with its own GET /destinations/{id} and only then read the rollover.
   * That second hop went out a round trip after the card mounted, so a row deleted in between answered it 404 --
   * a console error on the card, caught intermittently by the browser suite. The hop was also reading
   * `sourceId` off the top level of an envelope that carries it under `destination`, so it always came back null.
   * The status row the card is drawn from carries `sourceId` itself. */
  it("reads the rollover once at mount with the card's own programme, and asks for nothing else", async () => {
    const network = vi.fn();
    vi.stubGlobal("fetch", network);
    mocks.getRollover.mockResolvedValue(view({}));

    render(<RolloverPanel dest={dest} />);

    await waitFor(() => expect(mocks.getRollover).toHaveBeenCalledTimes(1));
    expect(mocks.getRollover).toHaveBeenCalledWith(7, 3);
    expect(network, "the panel made a request of its own besides the rollover read").not.toHaveBeenCalled();
  });
});

describe("RolloverPanel countdown", () => {
  it("counts down to a verified cutoff and labels the cap as reported", async () => {
    const cutoff = new Date(Date.now() + 2 * 3_600_000).toISOString();
    mocks.getRollover.mockResolvedValue(view({ cutoffAt: cutoff }));

    render(<RolloverPanel dest={dest} />);

    await waitFor(() => expect(screen.getByText(/until the platform's limit/)).toBeTruthy());
    expect(screen.getByText(/^[12]h \d\dm until the platform's limit$/)).toBeTruthy();
    expect(screen.getByText(/Reported by a secondary source/)).toBeTruthy();
    expect(mocks.getRollover).toHaveBeenCalledWith(7, 3);
  });

  it("waits for the platform's start when delivering with no cutoff yet", async () => {
    mocks.getRollover.mockResolvedValue(view({ cutoffAt: undefined }));

    // A row carrying no programme (omitempty on the wire) reads with no ?source rather than not at all.
    render(<RolloverPanel dest={{ ...dest, sourceId: undefined }} />);

    await waitFor(() => expect(screen.getByText(/Waiting for the platform/)).toBeTruthy());
    expect(mocks.getRollover).toHaveBeenCalledWith(7, null);
  });

  it("shows no number for an unverified cap, only the reason", async () => {
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
    mocks.getRollover.mockResolvedValue(view({ delivering: false }));

    render(<RolloverPanel dest={dest} />);

    await waitFor(() => expect(screen.getByText(/not delivering right now/)).toBeTruthy());
  });

  // A countdown-only platform (Twitch, Facebook) cannot be rolled over by polyemesis: the
  // server only counts the time down. Offering "Roll over now" there invites a refusal.
  it("offers Roll over now only on a platform polyemesis can roll over automatically", async () => {
    mocks.getRollover.mockResolvedValue(view({ action: "countdown" }));

    render(<RolloverPanel dest={dest} />);

    await waitFor(() => expect(screen.getByText(/Settings|Rollover settings/)).toBeTruthy());
    expect(screen.queryByRole("button", { name: "Roll over now" })).toBeNull();
  });

  it("offers Roll over now on an automatic platform while it is delivering", async () => {
    mocks.getRollover.mockResolvedValue(view({ action: "automatic", platform: "youtube" }));

    render(<RolloverPanel dest={dest} />);

    await waitFor(() => expect(screen.getByRole("button", { name: "Roll over now" })).toBeTruthy());
  });

  // A read that fails leaves no view to draw, but the failure itself must still be shown:
  // returning nothing would make a broken read look like a platform with no cap.
  it("shows a failed read as a notice rather than rendering nothing", async () => {
    mocks.getRollover.mockRejectedValue(new ApiError(500, "the rollover store could not be read"));

    render(<RolloverPanel dest={dest} />);

    await waitFor(() => expect(screen.getByRole("status").textContent).toContain("rollover store could not be read"));
  });

  it("renders nothing for a platform with no cap", async () => {
    mocks.getRollover.mockResolvedValue(view({ limit: undefined }));

    const { container } = render(<RolloverPanel dest={dest} />);

    await waitFor(() => expect(mocks.getRollover).toHaveBeenCalled());
    expect(container.textContent).toBe("");
  });
});
