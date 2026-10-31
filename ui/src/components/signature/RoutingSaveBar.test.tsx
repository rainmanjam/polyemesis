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

/** The bar's own status line; SaveStatus is a second, separate one. */
const bar = () => screen.getAllByRole("status")[0];

describe("RoutingSaveBar", () => {
  // A26: the bar is the page's only save surface, so it no longer vanishes
  // when clean. It says nothing is pending, greys both buttons, and links the
  // greyed Save changes to the sentence that says why (A28).
  it("stays, quiet and greyed, when nothing is pending", () => {
    render(<RoutingSaveBar {...base} dirty={false} reason="Nothing to save until a route changes." />);
    expect(bar().textContent).toContain("No unsaved changes.");
    expect(bar().textContent).not.toContain("YouTube main");
    const save = screen.getByRole("button", { name: "Save changes" }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    const discard = screen.getByRole("button", { name: /discard/i }) as HTMLButtonElement;
    expect(discard.disabled).toBe(true);
    const id = save.getAttribute("aria-describedby");
    expect(discard.getAttribute("aria-describedby")).toBe(id);
    expect(document.getElementById(id as string)?.textContent).toContain("Nothing to save");
  });

  it("carries the kit's SaveStatus beside its buttons", () => {
    render(<RoutingSaveBar {...base} saveState="failed" />);
    expect(document.querySelector("[data-save-state]")?.getAttribute("data-save-state")).toBe("failed");
  });

  it("names the destination the pending change applies to", () => {
    render(<RoutingSaveBar {...base} />);
    expect(bar().textContent).toContain("YouTube main");
  });

  it("warns that saving restarts an enabled destination", () => {
    // Saving an enabled destination restarts it with the new mix. That is the
    // one fact the operator must see before pressing Save, so the warning is
    // part of the bar, not a tooltip on the button.
    render(<RoutingSaveBar {...base} restarts />);
    const text = bar().textContent ?? "";
    expect(text).toContain("YouTube main");
    expect(text.toLowerCase()).toContain("restart");
  });

  it("says a level-only change applies live where supported, not that it restarts", () => {
    // Since I4 a gain or mute change is applied on the running process's
    // command channel. Warning of a restart there overstates the cost; a
    // Windows server still restarts, so the wording is "where supported".
    const { container } = render(<RoutingSaveBar {...base} restarts levelOnly />);
    const text = bar().textContent ?? "";
    expect(text).toContain("YouTube main");
    expect(text).toMatch(/live, without a restart, where the server supports it/);
    expect(container.firstElementChild?.className).not.toMatch(/border-warn/);
  });

  it("still warns of a restart for a structural change", () => {
    render(<RoutingSaveBar {...base} restarts levelOnly={false} />);
    expect(bar().textContent).toMatch(/^Saving restarts/);
  });

  it("does not claim a restart for a destination that is disabled", () => {
    render(<RoutingSaveBar {...base} restarts={false} />);
    expect(bar().textContent?.toLowerCase()).not.toContain("restart");
  });

  it("calls onSave only when Save is pressed, never on its own", () => {
    const onSave = vi.fn();
    const onDiscard = vi.fn();
    render(<RoutingSaveBar {...base} onSave={onSave} onDiscard={onDiscard} />);
    expect(onSave).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: /^save changes$/i }));
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
      (screen.getByRole("button", { name: /^save changes$/i }) as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  it("counts the pending changes and names the tracks they touch", () => {
    render(
      <RoutingSaveBar
        {...base}
        changes={[{ kind: "trackRemoved", track: 1 }, { kind: "trackLevel", track: 2 }, { kind: "ducking" }]}
      />,
    );
    expect(screen.getByTestId("save-bar-scope").textContent).toBe("Pending changes: 3. Tracks affected: 2, 3.");
    // Not on air: the scope is enough, no per-change lines.
    expect(screen.queryByTestId("save-bar-diff")).toBeNull();
  });

  it("spells out what changes on an enabled destination before it is applied", () => {
    render(
      <RoutingSaveBar
        {...base}
        restarts
        destinationName="Twitch"
        changes={[{ kind: "trackRemoved", track: 1 }, { kind: "matrix" }]}
      />,
    );
    const diff = screen.getByTestId("save-bar-diff").textContent ?? "";
    expect(diff).toContain("Track 2 will be removed from Twitch.");
    expect(diff).toContain("The channel matrix changes on Twitch.");
  });

  it("caps the live diff at four lines and counts the rest", () => {
    render(
      <RoutingSaveBar
        {...base}
        restarts
        changes={[1, 2, 3, 4, 5, 6].map((track) => ({ kind: "trackAdded" as const, track }))}
      />,
    );
    const items = screen.getByTestId("save-bar-diff").querySelectorAll("li");
    expect(items).toHaveLength(5);
    expect(items[4].textContent).toBe("Other changes: 2.");
  });
});
