// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, renderHook, waitFor } from "@testing-library/react";

import { useChatFeed } from "./useChatFeed";

/* #13/#21, the chat half: identical hazard to LiveDataProvider's, and fixed
 * the same way. `connected` cannot say a frame failed to parse -- the socket
 * stays open -- so a dropped chat message, state update or retraction was
 * invisible before this. Sticky rather than self-clearing, so a good frame
 * right afterwards does not quietly forgive the one that did not arrive.
 */

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      chatOverview: vi.fn().mockResolvedValue({
        configured: true,
        stored: false,
        statuses: [],
        limits: [],
        stats: null,
        messages: [],
      }),
    },
  };
});

/** A stand-in for jsdom, which does not implement WebSocket at all. */
class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  url: string;
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
  }
  close() {}
}

afterEach(() => {
  cleanup();
  FakeWebSocket.instances = [];
  vi.unstubAllGlobals();
});

describe("useChatFeed, on a frame the socket cannot be parsed", () => {
  it("flips frameError true and keeps it true past a good frame", async () => {
    vi.stubGlobal("WebSocket", FakeWebSocket);

    const { result, unmount } = renderHook(() => useChatFeed());

    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    const ws = FakeWebSocket.instances[0];
    ws.onopen?.();

    expect(result.current.frameError).toBe(false);

    ws.onmessage?.({ data: "{not valid json" });
    await waitFor(() => expect(result.current.frameError).toBe(true));

    ws.onmessage?.({ data: JSON.stringify({ type: "chatState", data: [] }) });
    expect(result.current.frameError).toBe(true);

    unmount();
  });
});
