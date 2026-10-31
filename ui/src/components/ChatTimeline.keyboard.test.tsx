// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import type { ChatMessage } from "@/lib/types";
import type { Asks } from "@/hooks/useConfirm";

/* Two accessibility defects on the same timeline rows.
 *
 * A44: the message menu (right-click, double-click) and the delete control were
 * pointer-only. Delete was display:none until the row was hovered, rows could
 * not take focus, and nothing opened the menu from the keyboard. A keyboard
 * user could not reach delete at all, and a touch user had no hover to reveal it.
 *
 * A45: the timeline is a role="log" with aria-live="polite", so a screen reader
 * announced every incoming chat line on a busy stream. The fix keeps the log
 * silent and announces the one thing worth hearing, the count of lines that
 * arrived while the reader was scrolled up, through a separate status region
 * that stays mounted so the first update is announced. */

const { ChatTimeline } = await import("./ChatPanel");

// Asks<T> is a branded confirm handle; the timeline only passes it through, so a
// bare mock is cast once here rather than branded in every test.
const asksMock = () => vi.fn() as unknown as Asks<ChatMessage>;

function message(id: string, text: string): ChatMessage {
  return {
    id,
    platform: "twitch",
    account: "twitch-acct",
    author: { id: `u-${id}`, name: `viewer${id}` },
    text,
    at: "2026-01-01T10:00:00Z",
  };
}

beforeEach(() => {
  if (!Element.prototype.scrollTo) {
    Element.prototype.scrollTo = function () {};
  }
});

afterEach(cleanup);

describe("a message row from the keyboard (A44)", () => {
  it("can take keyboard focus", () => {
    render(
      <ChatTimeline
        messages={[message("1", "hello there")]}
        onDelete={asksMock()}
        onMenu={vi.fn()}
      />,
    );
    const row = screen.getByText("hello there").closest("[tabindex]");
    expect(row, "no message row can be reached by Tab").not.toBeNull();
    expect(row?.getAttribute("tabindex")).toBe("0");
  });

  it("opens the quick menu on Enter, the ContextMenu key and Shift+F10", () => {
    const onMenu = vi.fn();
    render(
      <ChatTimeline messages={[message("1", "hello there")]} onDelete={asksMock()} onMenu={onMenu} />,
    );
    const row = screen.getByText("hello there").closest("[tabindex]") as HTMLElement;

    fireEvent.keyDown(row, { key: "Enter" });
    fireEvent.keyDown(row, { key: "ContextMenu" });
    fireEvent.keyDown(row, { key: "F10", shiftKey: true });

    expect(onMenu, "keyboard users have no way to open the message menu").toHaveBeenCalledTimes(3);
    expect(onMenu.mock.calls[0][0]).toMatchObject({ id: "1" });
  });

  it("does not open the menu for keys pressed on the name button inside the row", () => {
    const onMenu = vi.fn();
    render(
      <ChatTimeline messages={[message("1", "hello there")]} onDelete={asksMock()} onMenu={onMenu} />,
    );
    // The name is the button whose visible text is the author; the delete
    // control shares the author in its accessible name, so text is the handle.
    const name = screen.getByText("viewer1");
    fireEvent.keyDown(name, { key: "Enter" });
    expect(onMenu).not.toHaveBeenCalled();
  });

  it("shows delete when the row has focus, and on touch-only pointers", () => {
    render(
      <ChatTimeline messages={[message("1", "hello there")]} onDelete={asksMock()} onMenu={vi.fn()} />,
    );
    const del = screen.getByRole("button", { name: /delete/i });
    // Hover-only reveal left delete invisible to focus and to touch. The
    // classes are the contract: the button is shown by focus within the row,
    // and by a coarse pointer, as well as by hover.
    expect(del.className).toContain("group-focus-within:flex");
    expect(del.className).toContain("pointer-coarse:flex");
  });
});

describe("the timeline's live regions (A45)", () => {
  it("keeps the log itself silent so each incoming line is not announced", () => {
    render(<ChatTimeline messages={[message("1", "hello there")]} onDelete={asksMock()} />);
    const log = screen.getByRole("log");
    expect(
      log.getAttribute("aria-live"),
      "the timeline announces every new message on a busy stream",
    ).toBe("off");
  });

  it("has an always-mounted polite status region for the new-message count", () => {
    const { rerender } = render(<ChatTimeline messages={[]} onDelete={asksMock()} />);
    const status = screen.getByRole("status");
    expect(status.getAttribute("aria-live")).toBe("polite");
    expect(status.textContent).toBe("");
    rerender(<ChatTimeline messages={[message("1", "hello there")]} onDelete={asksMock()} />);
    expect(screen.getByRole("status")).toBe(status);
  });
});
