// @vitest-environment jsdom

import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useHeldStop, useSettledAction } from "@/hooks/useHeldStop";
import { FLIP_GUARD_MS, STOP_UNDO_MS } from "@/lib/destinationAction";

afterEach(cleanup);

function HeldHarness({ onCommit }: { onCommit: () => void }) {
  const held = useHeldStop(onCommit);
  return (
    <div>
      <span data-testid="state">{held.pending ? "pending" : "idle"}</span>
      <button onClick={held.hold}>hold</button>
      <button onClick={held.cancel}>cancel</button>
    </div>
  );
}

describe("useHeldStop", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  /* NOTHING REACHES THE SERVER UNTIL THE WINDOW CLOSES. This is the property
   * that makes it undo rather than a compensating "start it again": a stop
   * that was already sent cannot be taken back, because the broadcast it
   * ended cannot be resumed. */
  it("sends nothing while the hold is open", () => {
    const commit = vi.fn();
    render(<HeldHarness onCommit={commit} />);

    act(() => screen.getByText("hold").click());
    expect(screen.getByTestId("state").textContent).toBe("pending");

    act(() => void vi.advanceTimersByTime(STOP_UNDO_MS - 200));
    expect(commit).not.toHaveBeenCalled();
  });

  it("commits once the window closes", () => {
    const commit = vi.fn();
    render(<HeldHarness onCommit={commit} />);

    act(() => screen.getByText("hold").click());
    act(() => void vi.advanceTimersByTime(STOP_UNDO_MS + 200));

    expect(commit).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId("state").textContent).toBe("idle");
  });

  it("cancels for good, rather than deferring", () => {
    const commit = vi.fn();
    render(<HeldHarness onCommit={commit} />);

    act(() => screen.getByText("hold").click());
    act(() => void vi.advanceTimersByTime(1_000));
    act(() => screen.getByText("cancel").click());
    // Well past the original deadline. An undo that merely postponed the stop
    // would fire here, which is the worst of both designs.
    act(() => void vi.advanceTimersByTime(STOP_UNDO_MS * 3));

    expect(commit).not.toHaveBeenCalled();
    expect(screen.getByTestId("state").textContent).toBe("idle");
  });

  /* NAVIGATING AWAY IS NOT AN UNDO.
   *
   * The commit lived only in the interval, so unmounting inside the window
   * discarded the stop with no request, no toast and nothing on screen. The
   * card said "Stopping" until it stopped existing, and the destination
   * stayed live. */
  it("sends a stop that is still owed when the card goes away", () => {
    const commit = vi.fn();
    const { unmount } = render(<HeldHarness onCommit={commit} />);

    act(() => screen.getByText("hold").click());
    act(() => void vi.advanceTimersByTime(1_000));
    // The operator leaves the page well inside the undo window.
    unmount();

    expect(commit).toHaveBeenCalledTimes(1);
  });

  it("sends nothing on unmount once undo has been pressed", () => {
    const commit = vi.fn();
    const { unmount } = render(<HeldHarness onCommit={commit} />);

    act(() => screen.getByText("hold").click());
    act(() => screen.getByText("cancel").click());
    unmount();

    expect(commit).not.toHaveBeenCalled();
  });

  it("does not send twice when the window closed before the card did", () => {
    const commit = vi.fn();
    const { unmount } = render(<HeldHarness onCommit={commit} />);

    act(() => screen.getByText("hold").click());
    act(() => void vi.advanceTimersByTime(STOP_UNDO_MS + 200));
    unmount();

    expect(commit).toHaveBeenCalledTimes(1);
  });

  it("sends nothing on unmount when no stop was ever held", () => {
    const commit = vi.fn();
    const { unmount } = render(<HeldHarness onCommit={commit} />);
    unmount();
    expect(commit).not.toHaveBeenCalled();
  });
});

function SettledHarness({ enabled }: { enabled: boolean }) {
  const { action, unsettled } = useSettledAction(enabled);
  return <span data-testid="s">{`${action}:${unsettled ? "guarded" : "ready"}`}</span>;
}

/* REAL TIMERS, DELIBERATELY, and this is the whole reason the guard's own bug
 * survived its own test.
 *
 * With `vi.useFakeTimers()`, `Date.now` is faked too, and
 * `act(() => vi.advanceTimersByTime(750))` moves the clock 750ms forward
 * BEFORE React flushes the re-render queued from inside that block. So the
 * hook's wake-up -- which was scheduled for the elapsed time, i.e. ~0ms, and
 * fired immediately while still guarded -- recomputed `unsettled` against a
 * clock that had already jumped past FLIP_GUARD_MS, and read "ready". The
 * assertion passed on a re-render the fix is what actually produces.
 *
 * On a real clock the wake-up has to be scheduled for the REMAINING time or it
 * arrives too early, finds itself still guarded, and (with a dependency array
 * that cannot see "still guarded, but less so") never re-arms. That is the
 * button that stays dead for ever, and `waitFor` is what notices. */
describe("useSettledAction", () => {
  it("guards the button for a moment after the action flips", () => {
    const { rerender } = render(<SettledHarness enabled={false} />);

    // The socket repaint that inverts the button under the cursor.
    rerender(<SettledHarness enabled={true} />);
    expect(screen.getByTestId("s").textContent).toBe("stop:guarded");
  });

  it("settles on its own, without waiting for another repaint", async () => {
    // A card whose socket has gone quiet is exactly the one an operator is
    // trying to act on. If the guard only lifted on the next render, the
    // button would stay disabled indefinitely. NOTHING RE-RENDERS THIS
    // HARNESS after the flip: only the hook waking itself can move it.
    const { rerender } = render(<SettledHarness enabled={false} />);
    rerender(<SettledHarness enabled={true} />);
    expect(screen.getByTestId("s").textContent).toBe("stop:guarded");

    await waitFor(
      () => expect(screen.getByTestId("s").textContent).toBe("stop:ready"),
      { timeout: FLIP_GUARD_MS * 4, interval: 25 },
    );
  });

  it("is still guarded a moment before the window closes", async () => {
    // The other half: a wake-up armed for the remaining time must not lift the
    // guard early, or the flip window it exists to cover is not covered.
    const { rerender } = render(<SettledHarness enabled={false} />);
    rerender(<SettledHarness enabled={true} />);
    await new Promise((r) => setTimeout(r, FLIP_GUARD_MS / 2));
    expect(screen.getByTestId("s").textContent).toBe("stop:guarded");
  });

  it("does not restart the window on an unrelated repaint", async () => {
    // The socket sends a dozen things that re-render this card. Only a change
    // of ACTION is a reason to distrust a click.
    const { rerender } = render(<SettledHarness enabled={true} />);
    await waitFor(
      () => expect(screen.getByTestId("s").textContent).toBe("stop:ready"),
      { timeout: FLIP_GUARD_MS * 4, interval: 25 },
    );
    rerender(<SettledHarness enabled={true} />);
    expect(screen.getByTestId("s").textContent).toBe("stop:ready");
  });
});
