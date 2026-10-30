// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { RoutingSaveBar } from "./RoutingSaveBar";

/* THE SAVE SCOPE RULE (A7). The Save control used to sit in the page header,
 * out of sight once an operator had scrolled down to the mix they had edited.
 * While the form is dirty, the pending change is named in a bar that stays on
 * screen, says WHICH destination it applies to, warns when saving will restart
 * that destination, and keeps the apply step explicit (Save and Discard, never automatic). */
afterEach(cleanup);

const base = {
  dirty: true,
  saving: false,
  blocked: false,
  destinationName: "YouTube main",
  restarts: false,
  onSave: vi.fn(),
  onDiscard: vi.fn(),
};

describe("RoutingSaveBar", () => {
  it("is absent when nothing is pending", () => {
    render(<RoutingSaveBar {...base} dirty={false} />);
    expect(screen.queryByRole("status")).toBeNull();
    expect(screen.queryByRole("button", { name: /save/i })).toBeNull();
  });

  it("names the destination the pending change applies to", () => {
    render(<RoutingSaveBar {...base} />);
    expect(screen.getByRole("status").textContent).toContain("YouTube main");
  });

  it("warns that saving restarts an enabled destination", () => {
    // Saving an enabled destination restarts it with the new mix. That is the
    // one fact the operator must see before pressing Save, so the warning is
    // part of the bar, not a tooltip on the button.
    render(<RoutingSaveBar {...base} restarts />);
    const text = screen.getByRole("status").textContent ?? "";
    expect(text).toContain("YouTube main");
    expect(text.toLowerCase()).toContain("restart");
  });

  it("does not claim a restart for a destination that is disabled", () => {
    render(<RoutingSaveBar {...base} restarts={false} />);
    expect(screen.getByRole("status").textContent?.toLowerCase()).not.toContain("restart");
  });

  it("calls onSave only when Save is pressed, never on its own", () => {
    const onSave = vi.fn();
    const onDiscard = vi.fn();
    render(<RoutingSaveBar {...base} onSave={onSave} onDiscard={onDiscard} />);
    expect(onSave).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }));
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onDiscard).not.toHaveBeenCalled();
  });

  it("reverts the pending change on Discard", () => {
    const onSave = vi.fn();
    const onDiscard = vi.fn();
    render(<RoutingSaveBar {...base} onSave={onSave} onDiscard={onDiscard} />);
    fireEvent.click(screen.getByRole("button", { name: /discard/i }));
    expect(onDiscard).toHaveBeenCalledTimes(1);
    expect(onSave).not.toHaveBeenCalled();
  });

  it("keeps Save disabled while the form is blocked by a compile error", () => {
    render(<RoutingSaveBar {...base} blocked />);
    expect(
      (screen.getByRole("button", { name: /^save$/i }) as HTMLButtonElement).disabled,
    ).toBe(true);
  });
});
