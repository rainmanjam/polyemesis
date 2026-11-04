// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";

import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog";
import { useDismissGuard } from "./useDismissGuard";

afterEach(cleanup);

// The whole gesture on the scrim: Radix decides "outside" across pointerdown,
// pointerup and click, so firing one of them alone proves nothing.
function clickOverlay() {
  const scrim = document.querySelector(".fixed.inset-0") as Element;
  fireEvent.pointerDown(scrim, { button: 0, pointerType: "mouse" });
  fireEvent.mouseDown(scrim, { button: 0 });
  fireEvent.pointerUp(scrim, { button: 0, pointerType: "mouse" });
  fireEvent.mouseUp(scrim, { button: 0 });
  fireEvent.click(scrim, { button: 0 });
}

function Harness({ onBlocked }: { onBlocked: () => void }) {
  const [open, setOpen] = useState(true);
  const guard = useDismissGuard(onBlocked);
  return (
    <>
      <span data-testid="state">{open ? "open" : "closed"}</span>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent {...guard.contentProps}>
          <DialogTitle>Destination</DialogTitle>
          <input aria-label="stream key" />
        </DialogContent>
      </Dialog>
    </>
  );
}

describe("useDismissGuard (poka-yoke audit 2026-11-04)", () => {
  it("never closes on a click outside, even untouched", async () => {
    render(<Harness onBlocked={vi.fn()} />);
    // Radix registers its outside-pointer listener a tick after mounting.
    await act(() => new Promise((r) => setTimeout(r, 0)));
    clickOverlay();
    expect(screen.getByTestId("state").textContent).toBe("open");
  });

  // The control for the test above: without the guard, the same gesture does
  // close the dialog, so the assertion is about the guard and not about jsdom.
  it("(control) an unguarded dialog does close on that same click", async () => {
    function Bare() {
      const [open, setOpen] = useState(true);
      return (
        <>
          <span data-testid="state">{open ? "open" : "closed"}</span>
          <Dialog open={open} onOpenChange={setOpen}>
            <DialogContent>
              <DialogTitle>Destination</DialogTitle>
            </DialogContent>
          </Dialog>
        </>
      );
    }
    render(<Bare />);
    await act(() => new Promise((r) => setTimeout(r, 0)));
    clickOverlay();
    expect(screen.getByTestId("state").textContent).toBe("closed");
  });

  it("lets Esc close a dialog nobody has typed into", () => {
    render(<Harness onBlocked={vi.fn()} />);
    fireEvent.keyDown(screen.getByLabelText("stream key"), { key: "Escape" });
    expect(screen.getByTestId("state").textContent).toBe("closed");
  });

  it("refuses Esc once a field has been typed into, and says so", () => {
    const blocked = vi.fn();
    render(<Harness onBlocked={blocked} />);
    const key = screen.getByLabelText("stream key");
    fireEvent.change(key, { target: { value: "live_123" } });
    fireEvent.keyDown(key, { key: "Escape" });
    expect(screen.getByTestId("state").textContent).toBe("open");
    expect(blocked).toHaveBeenCalledTimes(1);
    expect((key as HTMLInputElement).value).toBe("live_123");
  });
});
