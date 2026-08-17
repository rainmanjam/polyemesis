// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";

import { AccountLiveStats } from "./AccountLiveStats";
import { VIEWER_POLL_MS } from "@/lib/viewerCount";

/* THE POLLING HALF OF AccountLiveStats, WHICH ITS SIBLING TEST CANNOT REACH.
 *
 * AccountLiveStats.test.tsx renders ViewerReadoutLine — the pure half, split
 * out of this file precisely so it could be rendered from a plain value — with
 * renderToStaticMarkup. That proves every readout renders the right sentence,
 * and it is the right technique for that question. It also cannot run an
 * effect, so the component that does the asking was untested end to end.
 *
 * WHAT LIVES IN THE UNTESTED HALF IS TWO QUOTA RULES, both written into the
 * source as warnings and neither checked:
 *
 *   "A backgrounded tab left open overnight would otherwise spend a YouTube
 *    project's whole daily quota on a number nobody is reading, and take title
 *    push down with it."
 *
 *   "A platform that cannot answer will not start answering, so once it has
 *    said so the polling stops."
 *
 * Both are silent when broken. Nothing goes red, no test goes out, and the bill
 * arrives as a platform disabling the project's API access — which takes title
 * push and stream-key fetch down with it, on an install nobody touched. That is
 * the failure this file exists to make loud.
 *
 * jsdom rather than the suite's node environment, for the reason set out in
 * useFacebookStreamHealth.test.tsx: these behaviours ARE effects, and
 * document.visibilityState is the input to one of them. */

const accountStats = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { accountStats: (id: number) => accountStats(id) } };
});

/** Drives document.visibilityState and fires the event the component listens
 *  for. jsdom reports "visible" and offers no setter, so the property is
 *  redefined rather than assigned. */
function setVisibility(state: "visible" | "hidden") {
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => state,
  });
  document.dispatchEvent(new Event("visibilitychange"));
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  accountStats.mockReset();
  setVisibility("visible");
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

async function tick(times = 1) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(VIEWER_POLL_MS * times + 1);
  });
}

const live = (viewers: number, title = "") => ({
  supported: true,
  stats: { viewerCount: viewers, title },
});

describe("AccountLiveStats polling", () => {
  it("says it is checking before the first answer, rather than showing a skeleton", async () => {
    // A skeleton that never resolves is indistinguishable from a broken panel,
    // which is why this state is a word. Asserted as "something is said", not
    // as an exact string, so translating the catalogue does not fail this.
    accountStats.mockReturnValue(new Promise(() => {}));

    const { container } = render(<AccountLiveStats accountId={3} />);

    expect(container.textContent?.trim()).not.toBe("");
  });

  it("shows the platform's own title beside the count, once it is live", async () => {
    accountStats.mockResolvedValue(live(1234, "Tonight's broadcast"));

    render(<AccountLiveStats accountId={3} />);

    await waitFor(() => expect(screen.getByText("Tonight's broadcast")).toBeDefined());
  });

  it("STOPS POLLING once the platform has said it cannot answer", async () => {
    // The quota rule. An unsupported platform will not start being supported,
    // so re-asking every minute is spent quota and a request an operator finds
    // in their access log wondering what it was for.
    accountStats.mockResolvedValue({ supported: false, stats: {} });

    render(<AccountLiveStats accountId={3} />);

    await waitFor(() => expect(accountStats).toHaveBeenCalled());
    const settledAfter = accountStats.mock.calls.length;

    await tick(3);

    expect(accountStats.mock.calls.length).toBe(settledAfter);
  });

  it("STOPS POLLING while the tab is hidden, and asks again on return", async () => {
    // The overnight-tab rule, and the reason it is two assertions rather than
    // one: stopping is only half the behaviour. Coming back must ask
    // IMMEDIATELY rather than waiting out the remainder of an interval, or an
    // operator switching back to the tab reads a stale count as current.
    accountStats.mockResolvedValue(live(10));

    render(<AccountLiveStats accountId={3} />);
    await waitFor(() => expect(accountStats).toHaveBeenCalled());

    await act(async () => setVisibility("hidden"));
    const whenHidden = accountStats.mock.calls.length;

    await tick(3);
    expect(accountStats.mock.calls.length).toBe(whenHidden);

    await act(async () => setVisibility("visible"));
    await waitFor(() =>
      expect(accountStats.mock.calls.length).toBeGreaterThan(whenHidden),
    );
  });

  it("keeps polling after a failed read, and does not go silent about it", async () => {
    // A 502 from the platform, or the poll racing a disconnect. Not a toast and
    // not a stop: the next tick recovers. What it must NOT do is leave the
    // previous number on screen, where it reads as current.
    accountStats.mockRejectedValueOnce(new Error("upstream is unwell"));
    accountStats.mockResolvedValue(live(77));

    const { container } = render(<AccountLiveStats accountId={3} />);

    await waitFor(() => expect(container.textContent?.trim()).not.toBe(""));
    const afterFailure = accountStats.mock.calls.length;

    await tick();

    // It recovered rather than settling on the failure.
    await waitFor(() =>
      expect(accountStats.mock.calls.length).toBeGreaterThan(afterFailure),
    );
  });

  it("stops asking once it is unmounted", async () => {
    accountStats.mockResolvedValue(live(10));

    const { unmount } = render(<AccountLiveStats accountId={3} />);
    await waitFor(() => expect(accountStats).toHaveBeenCalled());

    unmount();
    const afterUnmount = accountStats.mock.calls.length;

    await tick(3);

    expect(accountStats.mock.calls.length).toBe(afterUnmount);
  });
});
