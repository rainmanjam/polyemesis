// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ActivityEvent } from "@/lib/types";

const getActivity = vi.fn();
const replayActivity = vi.fn();
vi.mock("@/lib/api", async (orig) => {
  const real = await orig<typeof import("@/lib/api")>();
  return { ...real, api: { ...real.api, getActivity: (...a: unknown[]) => getActivity(...a), replayActivity: (...a: unknown[]) => replayActivity(...a) } };
});
vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({ programme: null, programmeKnown: true }),
}));

import { ApiError } from "@/lib/api";
import { useActivity } from "./useActivity";

class FakeSocket {
  static all: FakeSocket[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((e: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  url: string;
  constructor(url: string) {
    this.url = url;
    FakeSocket.all.push(this);
  }
  close() {
    this.closed = true;
    this.onclose?.();
  }
  open() {
    this.onopen?.();
  }
  send(type: string, data: unknown) {
    this.onmessage?.({ data: JSON.stringify({ type, data }) });
  }
}

const ev = (id: string, at: string, over: Partial<ActivityEvent> = {}): ActivityEvent => ({
  id,
  kind: "follow",
  platform: "twitch",
  at,
  source: { type: "t", id },
  ...over,
});

beforeEach(() => {
  FakeSocket.all = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  getActivity.mockReset();
  replayActivity.mockReset();
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("useActivity", () => {
  it("seeds from GET /activity?limit=200, then takes live frames", async () => {
    getActivity.mockResolvedValue({ enabled: true, events: [ev("b", "2026-11-07T20:00:02Z"), ev("a", "2026-11-07T20:00:01Z")] });
    const { result } = renderHook(() => useActivity());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(getActivity).toHaveBeenCalledWith({ limit: 200 });
    expect(result.current.enabled).toBe(true);
    expect(result.current.events.map((e) => e.id)).toEqual(["b", "a"]);

    const ws = FakeSocket.all[0];
    expect(ws.url).toMatch(/\/api\/v1\/ws$/);
    act(() => ws.open());
    expect(result.current.connected).toBe(true);
    act(() => ws.send("activity", ev("c", "2026-11-07T20:00:03Z")));
    expect(result.current.events.map((e) => e.id)).toEqual(["c", "b", "a"]);
    // a replay of the same id (seed/socket overlap) is not a second row
    act(() => ws.send("activity", ev("c", "2026-11-07T20:00:03Z")));
    expect(result.current.events).toHaveLength(3);
    // other frame types are not ours
    act(() => ws.send("chat", { id: "x" }));
    expect(result.current.events).toHaveLength(3);
  });

  it("is bounded to 500 rows", async () => {
    getActivity.mockResolvedValue({ enabled: true, events: [] });
    const { result } = renderHook(() => useActivity());
    await waitFor(() => expect(result.current.loading).toBe(false));
    const ws = FakeSocket.all[0];
    act(() => {
      for (let i = 0; i < 520; i++) ws.send("activity", ev(`e${i}`, new Date(Date.UTC(2026, 10, 7, 20, 0, 0) + i * 1000).toISOString()));
    });
    expect(result.current.events).toHaveLength(500);
    expect(result.current.events[0].id).toBe("e519");
  });

  it("keeps frames that arrive while the seed is in flight", async () => {
    let resolve!: (v: unknown) => void;
    getActivity.mockReturnValue(new Promise((r) => (resolve = r)));
    const { result } = renderHook(() => useActivity());
    const ws = FakeSocket.all[0];
    act(() => ws.send("activity", ev("live", "2026-11-07T20:00:05Z")));
    await act(async () => resolve({ enabled: true, events: [ev("old", "2026-11-07T20:00:01Z")] }));
    expect(result.current.events.map((e) => e.id)).toEqual(["live", "old"]);
  });

  it("reads the list again after a dropped socket reconnects", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    getActivity.mockResolvedValueOnce({ enabled: true, events: [ev("a", "2026-11-07T20:00:01Z")] });
    const { result } = renderHook(() => useActivity());
    await vi.waitFor(() => expect(result.current.loading).toBe(false));
    const first = FakeSocket.all[0];
    act(() => first.open());
    getActivity.mockResolvedValueOnce({ enabled: true, events: [ev("missed", "2026-11-07T20:00:02Z"), ev("a", "2026-11-07T20:00:01Z")] });
    act(() => first.onclose?.());
    expect(result.current.connected).toBe(false);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100);
    });
    const second = FakeSocket.all[1];
    expect(second).toBeDefined();
    act(() => second.open());
    await vi.waitFor(() => expect(result.current.events.map((e) => e.id)).toEqual(["missed", "a"]));
    expect(getActivity).toHaveBeenCalledTimes(2);
  });

  it("flags an unparseable frame and keeps running", async () => {
    getActivity.mockResolvedValue({ enabled: true, events: [] });
    const { result } = renderHook(() => useActivity());
    await waitFor(() => expect(result.current.loading).toBe(false));
    act(() => FakeSocket.all[0].onmessage?.({ data: "{not json" }));
    expect(result.current.frameError).toBe(true);
  });

  it("reports a read-scoped session as forbidden, not as an error", async () => {
    getActivity.mockRejectedValue(new ApiError(403, "forbidden"));
    const { result } = renderHook(() => useActivity());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.forbidden).toBe(true);
    expect(result.current.error).toBe("");
  });

  it("reports any other failure with the server's sentence", async () => {
    getActivity.mockRejectedValue(new ApiError(500, "boom"));
    const { result } = renderHook(() => useActivity());
    await waitFor(() => expect(result.current.error).toBe("boom"));
  });

  it("closes its socket when the last user goes", async () => {
    getActivity.mockResolvedValue({ enabled: true, events: [] });
    const { unmount } = renderHook(() => useActivity());
    await waitFor(() => expect(FakeSocket.all).toHaveLength(1));
    unmount();
    expect(FakeSocket.all[0].closed).toBe(true);
  });

  it("replay: ok, gone on 404, thrown otherwise", async () => {
    getActivity.mockResolvedValue({ enabled: true, events: [] });
    const { result } = renderHook(() => useActivity());
    await waitFor(() => expect(result.current.loading).toBe(false));
    replayActivity.mockResolvedValueOnce({ replayed: true });
    await expect(result.current.replay("a/b")).resolves.toBe("replayed");
    expect(replayActivity).toHaveBeenCalledWith("a/b");
    replayActivity.mockRejectedValueOnce(new ApiError(404, "gone"));
    await expect(result.current.replay("x")).resolves.toBe("gone");
    replayActivity.mockRejectedValueOnce(new ApiError(500, "bad"));
    await expect(result.current.replay("x")).rejects.toThrow("bad");
  });
});
