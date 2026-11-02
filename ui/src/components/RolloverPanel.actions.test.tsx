// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

import { ApiError } from "@/lib/api";

import { RolloverPanel } from "./RolloverPanel";
import type { DestStatus, RolloverView } from "@/lib/types";

/* #19's two actions on the destination card: saving the rollover settings, and rolling over now. A save must send
 * the settings the form shows, to the destination's own programme; a manual run goes through the confirmation and
 * each way the server can answer is told apart, because "the platform refused and the old broadcast is still
 * running" and "this destination is gone" call for different things from the operator. */

const mocks = vi.hoisted(() => ({
  getRollover: vi.fn(),
  saveRollover: vi.fn(),
  runRollover: vi.fn(),
}));

vi.mock("@/lib/recurringApi", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/recurringApi")>();
  return { ...real, ...mocks };
});

afterEach(() => {
  cleanup();
  for (const m of Object.values(mocks)) m.mockReset();
});

const dest = { id: 7, name: "YouTube main", platform: "youtube", enabled: true, sourceId: 3 } as unknown as DestStatus;

function view(over: Partial<RolloverView> = {}): RolloverView {
  return {
    settings: { destinationId: 7, enabled: false, marginMinutes: 30, chatNotice: false },
    platform: "youtube",
    action: "automatic",
    limit: { presetId: "youtube", kind: "archiveDuration", hours: 12, status: "documented" },
    delivering: true,
    due: false,
    cutoffAt: new Date(Date.now() + 3_600_000).toISOString(),
    ...over,
  };
}

async function openPanel() {
  mocks.getRollover.mockResolvedValue(view());
  render(<RolloverPanel dest={dest} />);
  await screen.findByRole("button", { name: "Rollover settings" });
}

async function confirmRun() {
  fireEvent.click(screen.getByRole("button", { name: "Roll over now" }));
  const dialog = await screen.findByRole("dialog");
  fireEvent.click(within(dialog).getByRole("button", { name: "Roll over now" }));
}

describe("RolloverPanel settings", () => {
  it("saves exactly the settings the form shows, to the destination's programme", async () => {
    await openPanel();
    mocks.saveRollover.mockResolvedValue(view({ settings: { destinationId: 7, enabled: true, marginMinutes: 45, chatNotice: true } }));

    fireEvent.click(screen.getByRole("button", { name: "Rollover settings" }));
    fireEvent.click(screen.getByRole("switch", { name: "Roll over before the limit" }));
    fireEvent.change(screen.getByLabelText("Safety margin, in minutes before the limit"), { target: { value: "45" } });
    fireEvent.change(screen.getByLabelText("Quiet window starts (HH:MM)"), { target: { value: "01:00" } });
    fireEvent.change(screen.getByLabelText("Quiet window ends (HH:MM)"), { target: { value: "03:00" } });
    fireEvent.change(screen.getByLabelText("Time zone for the quiet window"), { target: { value: "Europe/London" } });
    fireEvent.change(screen.getByLabelText("OBS scene that triggers a rollover"), { target: { value: "BRB" } });
    fireEvent.click(screen.getByRole("switch", { name: "Post a short notice in chat before a rollover" }));
    fireEvent.click(screen.getByRole("button", { name: "Save rollover settings" }));

    await waitFor(() => expect(mocks.saveRollover).toHaveBeenCalledTimes(1));
    expect(mocks.saveRollover).toHaveBeenCalledWith(7, 3, {
      enabled: true,
      marginMinutes: 45,
      quietStart: "01:00",
      quietEnd: "03:00",
      quietZone: "Europe/London",
      obsScene: "BRB",
      chatNotice: true,
    });
    expect((await screen.findByRole("status")).textContent).toBe("Rollover settings saved.");
  });

  it("shows the server's refusal of a save in its own words", async () => {
    await openPanel();
    mocks.saveRollover.mockRejectedValue(new ApiError(400, "the safety margin must be between 5 and 120 minutes"));

    fireEvent.click(screen.getByRole("button", { name: "Rollover settings" }));
    fireEvent.click(screen.getByRole("button", { name: "Save rollover settings" }));

    expect((await screen.findByRole("status")).textContent).toBe("the safety margin must be between 5 and 120 minutes");
  });
});

describe("RolloverPanel roll over now", () => {
  it("rolls over only after the confirmation, and says it is done", async () => {
    await openPanel();
    mocks.runRollover.mockResolvedValue({ status: "rolled over" });

    fireEvent.click(screen.getByRole("button", { name: "Roll over now" }));
    expect(mocks.runRollover).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Roll over now" }));

    await waitFor(() => expect(mocks.runRollover).toHaveBeenCalledWith(7, 3));
    expect((await screen.findByRole("status")).textContent).toBe("Rolled over. The new broadcast is in place.");
  });

  // MUTATION: the 502 branch reads the refused copy. Observed: FAIL, "Not rolled over: ..." for a platform refusal.
  it.each([
    [new ApiError(409, "the destination is not delivering"), "Not rolled over: the destination is not delivering"],
    [new ApiError(502, "youtube said no"), "The platform refused the rollover. The old broadcast is still running."],
    [new ApiError(404, "no such destination"), "This destination no longer exists."],
    [new ApiError(500, "boom"), "Polyemesis could not run the rollover. The log has the details."],
    [new ApiError(418, "teapot"), "Unexpected answer from the server (418)."],
  ])("tells the operator what a %s means", async (err, want) => {
    await openPanel();
    mocks.runRollover.mockRejectedValue(err);

    await confirmRun();

    await waitFor(() => expect(screen.getByRole("status").textContent).toBe(want));
    // The card is reread afterwards, so a refusal that changed nothing is shown against the current state.
    await waitFor(() => expect(mocks.getRollover.mock.calls.length).toBeGreaterThanOrEqual(2));
  });
});
