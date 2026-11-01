// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";

import { ApiError } from "@/lib/api";

import { RecurringCalendar } from "./RecurringCalendar";
import type { OccurrenceView } from "@/lib/types";

/* #18 calendar. Only an upcoming occurrence with a platform event can be skipped or retitled from here. A
 * pending one is still being created, so its row offers neither, and says it has no event yet. */

const mocks = vi.hoisted(() => ({ listOccurrences: vi.fn(), retitleOccurrence: vi.fn() }));

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

vi.mock("@/lib/recurringApi", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/recurringApi")>();
  return { ...real, listOccurrences: mocks.listOccurrences, retitleOccurrence: mocks.retitleOccurrence };
});

afterEach(() => {
  cleanup();
  mocks.listOccurrences.mockReset();
  mocks.retitleOccurrence.mockReset();
  vi.mocked(toast.error).mockReset();
});

function occ(over: Partial<OccurrenceView>): OccurrenceView {
  return {
    id: 1,
    scheduleId: 1,
    destinationId: 1,
    at: new Date(Date.now() + 86_400_000).toISOString(),
    state: "upcoming",
    title: "Tuesday live 2026-11-03",
    eventId: "yt-occ-1",
    ...over,
  };
}

describe("RecurringCalendar", () => {
  it("offers skip and edit title on an upcoming occurrence that has a platform event", async () => {
    mocks.listOccurrences.mockResolvedValue([occ({})]);

    render(<RecurringCalendar />);

    await waitFor(() => expect(screen.getByText("Tuesday live 2026-11-03")).toBeTruthy());
    expect(screen.getByRole("button", { name: "Skip this one" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Edit title" })).toBeTruthy();
  });

  it("offers neither action on a pending occurrence, and says there is no event yet", async () => {
    mocks.listOccurrences.mockResolvedValue([occ({ state: "pending", eventId: "", title: "" })]);

    render(<RecurringCalendar />);

    await waitFor(() => expect(screen.getByText("No platform event yet.")).toBeTruthy());
    expect(screen.queryByRole("button", { name: "Skip this one" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Edit title" })).toBeNull();
  });

  it("says so plainly when the window has nothing in it", async () => {
    mocks.listOccurrences.mockResolvedValue([]);

    render(<RecurringCalendar />);

    await waitFor(() => expect(screen.getByText("No upcoming events in the next two weeks.")).toBeTruthy());
  });

  it("shows a read failure as an alert rather than an empty calendar", async () => {
    mocks.listOccurrences.mockRejectedValue(new Error("the calendar window must run forward"));

    render(<RecurringCalendar />);

    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("calendar window"));
  });

  // A refused retitle must read as a retitle, not as a skip. "Not removed" on a title edit
  // tells the operator the wrong thing happened.
  async function retitleAndFail(err: unknown) {
    mocks.listOccurrences.mockResolvedValue([occ({})]);
    mocks.retitleOccurrence.mockRejectedValue(err);
    render(<RecurringCalendar />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit title" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Edit title" }), { target: { value: "New title" } });
    fireEvent.click(screen.getByRole("button", { name: "Save title" }));
    await waitFor(() => expect(toast.error).toHaveBeenCalledTimes(1));
    return String(vi.mocked(toast.error).mock.calls[0][0]);
  }

  it("reports a 409 on a retitle as a refused title change, with the server's reason", async () => {
    const msg = await retitleAndFail(new ApiError(409, "the show has started"));
    expect(msg).toBe("Title not changed: the show has started");
    expect(msg).not.toContain("Not removed");
  });

  it("reports a 502 on a retitle as the platform refusing the title change", async () => {
    const msg = await retitleAndFail(new ApiError(502, "upstream said no"));
    expect(msg).toBe("The platform refused the new title. Nothing changed here; try again later.");
    expect(msg).not.toContain("delete");
  });
});
