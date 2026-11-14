// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";

import { LiveDataProvider } from "./LiveDataProvider";
import { useLiveData } from "@/hooks/useLiveData";

/* The status socket is install-wide: every programme's engine publishes onto
 * it, and each frame carries only the renditions THAT engine runs. Replacing
 * status.renditions wholesale made the Renditions page show whichever
 * programme spoke last, so every encode belonging to another programme read
 * "Not encoding" while it was encoding. This pins the fold: a frame replaces
 * its own programme's renditions and leaves the others'. */

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      status: vi.fn().mockRejectedValue(new Error("no network in test")),
      source: vi.fn().mockRejectedValue(new Error("no network in test")),
      stats: vi.fn().mockRejectedValue(new Error("no network in test")),
    },
  };
});

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

function Probe() {
  const { status } = useLiveData();
  return (
    <span data-testid="rends">
      {(status?.renditions ?? []).map((r) => `${r.id}:${r.process ? "up" : "idle"}`).join(",")}
    </span>
  );
}

const frame = (sourceId: number, renditions: object[]) => ({
  data: JSON.stringify({
    type: "status",
    data: { source: { id: sourceId }, destinations: [], renditions },
  }),
});

afterEach(() => {
  cleanup();
  FakeWebSocket.instances = [];
  vi.unstubAllGlobals();
});

describe("LiveDataProvider, status frames from more than one programme", () => {
  it("keeps one programme's renditions when another programme reports", async () => {
    vi.stubGlobal("WebSocket", FakeWebSocket);
    render(
      <LiveDataProvider>
        <Probe />
      </LiveDataProvider>,
    );
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    const ws = FakeWebSocket.instances[0];
    const up = { state: "running" };

    act(() => {
      ws.onmessage?.(frame(1, [{ id: 10, process: up }]));
      ws.onmessage?.(frame(2, [{ id: 20, process: up }]));
    });
    expect(screen.getByTestId("rends").textContent).toBe("10:up,20:up");

    // A programme's own frame replaces ITS renditions, including with none.
    act(() => {
      ws.onmessage?.(frame(1, []));
    });
    expect(screen.getByTestId("rends").textContent).toBe("20:up");
  });
});
