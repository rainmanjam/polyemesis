// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";

import { useFacebookStreamHealth } from "./useFacebookStreamHealth";
import { ApiError } from "@/lib/api";
import { FACEBOOK_STREAM_HEALTH_INTERVAL_MS } from "@/lib/types";

/* THE ONLY FILE IN ui/src WITH ITS OWN ENVIRONMENT, AND THE REASON IS WORTH
 * STATING RATHER THAN LEAVING TO WHOEVER FINDS IT.
 *
 * vitest.config.ts sets environment: "node" for the whole suite, deliberately —
 * most of these tests read the Go tree to check a mirror has not drifted, and
 * several render components with renderToStaticMarkup, neither of which wants a
 * DOM. Switching the suite to jsdom to test one hook would put a fake DOM under
 * sixteen files that do not want one.
 *
 * This hook cannot be tested without one. Its whole behaviour lives in a
 * useEffect: it polls, it decides when to STOP polling, and it tears the
 * interval down on unmount. renderToStaticMarkup — the technique the component
 * tests beside it use — does not run effects at all, so it would render the
 * initial state and prove nothing.
 *
 * WHAT WAS ACTUALLY UNTESTED HERE. The pane is polled, so the expensive
 * mistakes are not "does it show the number" but "how often does it ask, and
 * when does it stop". Two of them are cheap to get wrong and invisible in
 * review: retrying a route that is never coming back, once every two seconds
 * for the life of the tab; and treating one bad tick as permanent, which
 * replaces a healthy reading with a red box two seconds before the next tick
 * would have recovered it. The hook's comments commit to both. Nothing checked
 * either. */

const facebookStreamHealth = vi.fn();
vi.mock("@/lib/api", async () => {
  // ApiError is a real class the hook does `instanceof` against, so it must be
  // the genuine one — a stub shape would make the 404 branch unreachable and
  // the "poll stops" test would pass for the wrong reason.
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { facebookStreamHealth: (id: number) => facebookStreamHealth(id) } };
});

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  facebookStreamHealth.mockReset();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

/** Advance past exactly one poll interval. */
async function tick(times = 1) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(FACEBOOK_STREAM_HEALTH_INTERVAL_MS * times + 1);
  });
}

describe("useFacebookStreamHealth", () => {
  it("asks nothing at all when it is not enabled", async () => {
    // `enabled` is what keeps this poll off every other card on the dashboard.
    // If it leaked, every destination on the page would poll a Facebook route
    // every two seconds — including the ones that are not Facebook.
    const { result } = renderHook(() => useFacebookStreamHealth(7, false));

    await tick(3);

    expect(facebookStreamHealth).not.toHaveBeenCalled();
    expect(result.current.kind).toBe("loading");
  });

  it("reports the streams the server answered with", async () => {
    facebookStreamHealth.mockResolvedValue({
      supported: true,
      streams: [{ id: "ingest-1", health: { video_bitrate: 5400 } }],
    });

    const { result } = renderHook(() => useFacebookStreamHealth(7, true));

    await waitFor(() => expect(result.current.kind).toBe("ok"));
    expect(result.current).toEqual({
      kind: "ok",
      streams: [{ id: "ingest-1", health: { video_bitrate: 5400 } }],
    });
  });

  it("treats a supported answer with no streams as an answer, not as nothing", async () => {
    // An empty list is a real reading: a scheduled broadcast has no ingest yet,
    // and a live one reports nothing until Facebook's own four-second timeout.
    // `streams ?? []` is what keeps this out of the error branch.
    facebookStreamHealth.mockResolvedValue({ supported: true });

    const { result } = renderHook(() => useFacebookStreamHealth(7, true));

    await waitFor(() => expect(result.current.kind).toBe("ok"));
    expect(result.current).toEqual({ kind: "ok", streams: [] });
  });

  it("distinguishes 'this platform publishes none' from a failure", async () => {
    facebookStreamHealth.mockResolvedValue({ supported: false });

    const { result } = renderHook(() => useFacebookStreamHealth(7, true));

    await waitFor(() => expect(result.current.kind).toBe("unsupported"));
  });

  // The expensive one. A 404 or 501 is not a blip — the route is not coming
  // back before a redeploy, and retrying it is one request every two seconds
  // for as long as the tab is open, against a server that has already said no.
  //
  // Both statuses, because the hook names both and they arrive from different
  // places: a 404 is a build without the route, a 501 is a build that has it
  // and has switched the capability off.
  it.each([404, 501])(
    "STOPS POLLING after a %d, rather than retrying a route that is not coming back",
    async (status) => {
      facebookStreamHealth.mockRejectedValue(new ApiError(status, "no such route"));

      const { result } = renderHook(() => useFacebookStreamHealth(7, true));

      await waitFor(() => expect(result.current.kind).toBe("unavailable"));
      const afterFirst = facebookStreamHealth.mock.calls.length;

      await tick(4);

      expect(facebookStreamHealth.mock.calls.length).toBe(afterFirst);
    },
  );

  it("does NOT stop for a server error, which is the status that does recover", async () => {
    // 500 sits either side of the line on purpose: it is an ApiError like the
    // two above, so a branch written as `err instanceof ApiError` without the
    // status check would swallow it and stop polling a server that is merely
    // restarting.
    facebookStreamHealth.mockRejectedValue(new ApiError(500, "upstream is unwell"));

    const { result } = renderHook(() => useFacebookStreamHealth(7, true));

    await waitFor(() => expect(result.current.kind).toBe("error"));
    const afterFirst = facebookStreamHealth.mock.calls.length;

    await tick(2);

    expect(facebookStreamHealth.mock.calls.length).toBeGreaterThan(afterFirst);
  });

  it("KEEPS POLLING after an ordinary failure, and recovers on the next tick", async () => {
    // The inverse, and equally deliberate. One bad tick must not be permanent:
    // stopping here would leave an operator looking at an error box on a stream
    // that came back two seconds later, with nothing to do but reload the page.
    facebookStreamHealth.mockRejectedValueOnce(new Error("network blip"));
    facebookStreamHealth.mockResolvedValue({ supported: true, streams: [] });

    const { result } = renderHook(() => useFacebookStreamHealth(7, true));

    await waitFor(() => expect(result.current.kind).toBe("error"));
    // It says what went wrong rather than going quiet, which would leave a
    // stale reading on screen reading as current.
    expect(result.current).toMatchObject({ kind: "error", detail: "network blip" });

    await tick();

    await waitFor(() => expect(result.current.kind).toBe("ok"));
  });

  it("stops asking once it is unmounted", async () => {
    // The cleanup. Without it the interval outlives the card, and a dashboard
    // that has opened and closed a few destination dialogs is polling for all
    // of them at once.
    facebookStreamHealth.mockResolvedValue({ supported: true, streams: [] });

    const { unmount } = renderHook(() => useFacebookStreamHealth(7, true));
    await waitFor(() => expect(facebookStreamHealth).toHaveBeenCalled());

    unmount();
    const afterUnmount = facebookStreamHealth.mock.calls.length;

    await tick(3);

    expect(facebookStreamHealth.mock.calls.length).toBe(afterUnmount);
  });

  it("polls again on the interval while everything is working", async () => {
    // The floor is Facebook's own published number, and asking faster spends
    // somebody's rate limit on numbers that have not changed. This asserts the
    // poll happens, not that it is throttled — the interval constant is the
    // subject of a test beside it.
    facebookStreamHealth.mockResolvedValue({ supported: true, streams: [] });

    renderHook(() => useFacebookStreamHealth(7, true));
    await waitFor(() => expect(facebookStreamHealth).toHaveBeenCalledTimes(1));

    await tick();

    await waitFor(() => expect(facebookStreamHealth.mock.calls.length).toBeGreaterThan(1));
  });
});
