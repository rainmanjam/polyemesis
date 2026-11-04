// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* A stray scrim click or Esc closed the four-step destination form with every
 * field gone, stream key included (poka-yoke audit 2026-11-04). The hook has
 * its own tests; this one proves DestinationDialog is wired to it. */

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      listSources: () => Promise.resolve([{ id: 1, name: "Main", publishUrls: {}, isDefault: true }]),
      listAccounts: () => Promise.resolve([]),
      listRenditions: () => Promise.resolve([]),
      listPlatformPresets: () => Promise.resolve([]),
      platformGuides: () => Promise.resolve([]),
      services: () => Promise.resolve({}),
    },
  };
});

import { DestinationDialog } from "./DestinationDialog";

beforeAll(() => {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.releasePointerCapture = () => {};
  window.HTMLElement.prototype.setPointerCapture = () => {};
  window.HTMLElement.prototype.scrollIntoView = () => {};
});

afterEach(cleanup);

function openNew(onOpenChange: (o: boolean) => void) {
  return render(
    <DestinationDialog open onOpenChange={onOpenChange} destination={null} onSaved={() => {}} />,
    { wrapper: MemoryRouter },
  );
}

function clickOverlay() {
  const scrim = document.querySelector(".fixed.inset-0") as Element;
  fireEvent.pointerDown(scrim, { button: 0, pointerType: "mouse" });
  fireEvent.mouseDown(scrim, { button: 0 });
  fireEvent.pointerUp(scrim, { button: 0, pointerType: "mouse" });
  fireEvent.mouseUp(scrim, { button: 0 });
  fireEvent.click(scrim, { button: 0 });
}

describe("the destination dialog keeps a half-filled form", () => {
  it("does not close on a click on the scrim", async () => {
    const onOpenChange = vi.fn();
    openNew(onOpenChange);
    await act(() => new Promise((r) => setTimeout(r, 0)));
    clickOverlay();
    expect(onOpenChange).not.toHaveBeenCalledWith(false);
  });

  it("does not close on Esc once something has been typed", async () => {
    const onOpenChange = vi.fn();
    openNew(onOpenChange);
    const field = (await screen.findAllByRole("textbox"))[0];
    fireEvent.change(field, { target: { value: "typed" } });
    fireEvent.keyDown(field, { key: "Escape" });
    expect(onOpenChange).not.toHaveBeenCalledWith(false);
  });
});
