// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

/* Expert mode's Clear used to fire DELETE /destinations/{id}/expert on one
 * click. The server saves empty args and reconciles, the spec hash changes,
 * and a live output restarts -- from a ghost button sitting beside Apply,
 * which itself takes resolve-then-apply to reach. A write that restarts a
 * live stream asks first and names the destination it is about to touch. */

const deleteExpert = vi.fn();

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      listDestinations: vi.fn().mockResolvedValue([
        { destination: { id: 3, name: "YouTube main" } },
      ]),
      getExpert: vi.fn().mockResolvedValue({
        args: { inputArgs: "", outputArgs: "-muxdelay 0.1", ackReencode: false },
        guards: [],
      }),
      deleteExpert: (...a: unknown[]) => {
        deleteExpert(...a);
        return Promise.resolve({
          args: { inputArgs: "", outputArgs: "", ackReencode: false },
          guards: [],
        });
      },
    },
  };
});

import { ExpertPanel } from "./MonitoringPage";

beforeAll(() => {
  // Radix Select measures and captures pointers; jsdom implements neither.
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.releasePointerCapture = () => {};
  window.HTMLElement.prototype.setPointerCapture = () => {};
  window.HTMLElement.prototype.scrollIntoView = () => {};
});

afterEach(() => {
  cleanup();
  deleteExpert.mockReset();
});

async function chooseDestination() {
  render(<ExpertPanel />);
  fireEvent.click(await screen.findByRole("combobox"));
  fireEvent.click(await screen.findByRole("option", { name: "YouTube main" }));
  await screen.findByDisplayValue("-muxdelay 0.1");
}

describe("expert mode's Clear", () => {
  it("does not delete on the first click", async () => {
    await chooseDestination();
    fireEvent.click(screen.getByRole("button", { name: "Clear" }));
    await new Promise((r) => setTimeout(r, 20));
    expect(deleteExpert).not.toHaveBeenCalled();
    // The dialog names the destination, so the wrong row is visible first.
    const dialog = await screen.findByRole("dialog");
    expect(dialog.textContent).toContain("YouTube main");
  });

  it("deletes once the operator confirms", async () => {
    await chooseDestination();
    fireEvent.click(screen.getByRole("button", { name: "Clear" }));
    await screen.findByRole("dialog");
    fireEvent.click(screen.getByRole("button", { name: "Clear arguments" }));
    await waitFor(() => expect(deleteExpert).toHaveBeenCalledWith(3));
  });
});
