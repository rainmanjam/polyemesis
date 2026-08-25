// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";

import { LiveDataProvider } from "./LiveDataProvider";
import { useLiveData } from "@/hooks/useLiveData";

/* #13/#21: a malformed WebSocket frame used to be dropped with a bare
 * `catch { return; }` -- no signal at all. `connected` cannot stand in for
 * this: onclose never fires, the socket stays open, and every other value on
 * screen keeps looking current while whatever that frame carried (a status, a
 * level reading, a log line) silently never lands. This pins the one thing an
 * operator actually needs: something readable through the public hook flips
 * to true the moment a frame fails to parse, and it does not flip back just
 * because the next frame is fine -- a console that already ate one message is
 * worth doubting for the rest of the session, not forgiven the instant it
 * recovers.
 */

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

/** A stand-in for jsdom, which does not implement WebSocket at all. Captures
 *  the instance the provider creates so the test can drive its handlers by
 *  hand, the way the real socket would. */
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
  const { frameError } = useLiveData();
  return <span data-testid="frame-error">{String(frameError)}</span>;
}

afterEach(() => {
  cleanup();
  FakeWebSocket.instances = [];
  vi.unstubAllGlobals();
});

describe("LiveDataProvider, on a frame the socket cannot be parsed", () => {
  it("flips frameError true and does not clear it on the next good frame", async () => {
    vi.stubGlobal("WebSocket", FakeWebSocket);

    render(
      <LiveDataProvider>
        <Probe />
      </LiveDataProvider>,
    );

    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    const ws = FakeWebSocket.instances[0];
    ws.onopen?.();

    expect(screen.getByTestId("frame-error").textContent).toBe("false");

    ws.onmessage?.({ data: "{not valid json" });
    await waitFor(() => expect(screen.getByTestId("frame-error").textContent).toBe("true"));

    // A clean frame right afterwards must not paper over the one that was
    // lost -- the whole point of the flag being sticky.
    ws.onmessage?.({ data: JSON.stringify({ type: "status", data: null }) });
    expect(screen.getByTestId("frame-error").textContent).toBe("true");
  });
});
