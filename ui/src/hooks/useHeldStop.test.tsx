// @vitest-environment jsdom

import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useHeldStop, useSettledAction } from "@/hooks/useHeldStop";
import { FLIP_GUARD_MS, STOP_UNDO_MS } from "@/lib/destinationAction";

afterEach(cleanup);
beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

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
});

function SettledHarness({ enabled }: { enabled: boolean }) {
  const { action, unsettled } = useSettledAction(enabled);
  return <span data-testid="s">{`${action}:${unsettled ? "guarded" : "ready"}`}</span>;
}

describe("useSettledAction", () => {
  it("guards the button for a moment after the action flips", () => {
    const { rerender } = render(<SettledHarness enabled={false} />);
    act(() => void vi.advanceTimersByTime(FLIP_GUARD_MS * 2));
    expect(screen.getByTestId("s").textContent).toBe("start:ready");

    // The socket repaint that inverts the button under the cursor.
    rerender(<SettledHarness enabled={true} />);
    expect(screen.getByTestId("s").textContent).toBe("stop:guarded");
  });

  it("settles on its own, without waiting for another repaint", () => {
    // A card whose socket has gone quiet is exactly the one an operator is
    // trying to act on. If the guard only lifted on the next render, the
    // button would stay disabled indefinitely.
    const { rerender } = render(<SettledHarness enabled={false} />);
    rerender(<SettledHarness enabled={true} />);
    expect(screen.getByTestId("s").textContent).toBe("stop:guarded");

    act(() => void vi.advanceTimersByTime(FLIP_GUARD_MS + 50));
    expect(screen.getByTestId("s").textContent).toBe("stop:ready");
  });

  it("does not restart the window on an unrelated repaint", () => {
    // The socket sends a dozen things that re-render this card. Only a change
    // of ACTION is a reason to distrust a click.
    const { rerender } = render(<SettledHarness enabled={true} />);
    act(() => void vi.advanceTimersByTime(FLIP_GUARD_MS + 50));
    rerender(<SettledHarness enabled={true} />);
    expect(screen.getByTestId("s").textContent).toBe("stop:ready");
  });
});
