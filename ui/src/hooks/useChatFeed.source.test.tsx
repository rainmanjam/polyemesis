// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";

import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import { useChatFeed } from "./useChatFeed";

/* The chat socket has to NAME A PROGRAMME, exactly as LiveDataProvider's does.
 *
 * /api/v1/ws resolves its engine through scopedEngine, which refuses an
 * unnamed upgrade with 400 source_required on any install with two or more
 * sources. The chat hook opened `/api/v1/ws` bare, so on a multi-source
 * install the upgrade failed, onclose fired, the backoff rescheduled it, and
 * the chat page sat on "socket offline" for the life of the tab -- while the
 * REST scrollback loaded fine, which is what made it read as a live-chat
 * outage rather than a client bug.
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

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  url: string;
  closed = false;
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
  }
  close() {
    this.closed = true;
  }
}

afterEach(() => {
  cleanup();
  FakeWebSocket.instances = [];
  vi.unstubAllGlobals();
});

function wrapperFor(live: Pick<LiveData, "programme" | "programmeKnown">) {
  return ({ children }: { children: ReactNode }) => (
    <LiveDataContext.Provider value={live as LiveData}>{children}</LiveDataContext.Provider>
  );
}

describe("useChatFeed's socket", () => {
  it("names the programme the console is looking at", async () => {
    vi.stubGlobal("WebSocket", FakeWebSocket);
    const { unmount } = renderHook(() => useChatFeed(), {
      wrapper: wrapperFor({ programme: 7, programmeKnown: true }),
    });
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    expect(FakeWebSocket.instances[0].url).toMatch(/\/api\/v1\/ws\?source=7$/);
    unmount();
  });

  it("opens nothing until the programme question is answered", async () => {
    vi.stubGlobal("WebSocket", FakeWebSocket);
    const { unmount } = renderHook(() => useChatFeed(), {
      wrapper: wrapperFor({ programme: null, programmeKnown: false }),
    });
    // An unnamed socket opened in this window is refused on a multi-source
    // install; waiting is what LiveDataProvider does for the same reason.
    await new Promise((r) => setTimeout(r, 20));
    expect(FakeWebSocket.instances).toHaveLength(0);
    unmount();
  });

  it("moves to the new programme when the operator switches", async () => {
    vi.stubGlobal("WebSocket", FakeWebSocket);
    let live = { programme: 7 as number | null, programmeKnown: true };
    const Wrapper = ({ children }: { children: ReactNode }) => (
      <LiveDataContext.Provider value={live as LiveData}>{children}</LiveDataContext.Provider>
    );
    const { rerender, unmount } = renderHook(() => useChatFeed(), { wrapper: Wrapper });
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));

    live = { programme: 9, programmeKnown: true };
    rerender();
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(2));
    expect(FakeWebSocket.instances[0].closed).toBe(true);
    expect(FakeWebSocket.instances[1].url).toMatch(/\?source=9$/);

    // The retired socket's close must not clobber the live one or schedule a
    // reconnect aimed at the programme the operator left.
    FakeWebSocket.instances[0].onclose?.();
    await new Promise((r) => setTimeout(r, 20));
    expect(FakeWebSocket.instances).toHaveLength(2);
    unmount();
  });
});
