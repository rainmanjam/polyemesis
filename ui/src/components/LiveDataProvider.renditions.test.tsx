// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { LiveDataProvider } from "./LiveDataProvider";
import { useLiveData } from "@/hooks/useLiveData";
import { api } from "@/lib/api";
import { Dashboard } from "@/pages/Dashboard";
import { translate } from "@/lib/i18n";

/* The status socket is install-wide: every programme's engine publishes onto
 * it, and each frame carries only the renditions THAT engine runs. Two readers
 * want different things from that:
 *
 *  - the Renditions page lists every programme's renditions and needs each one's
 *    live state, so it reads `allRenditions`, folded per programme;
 *  - the Dashboard's Pipeline card is about the selected programme, so it reads
 *    `status.renditions`, which stays the frame's own.
 *
 * The first fix folded the list into `status` and the Pipeline card then drew
 * every programme's ladder under the selected one. */

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      status: vi.fn().mockRejectedValue(new Error("no network in test")),
      source: vi.fn().mockRejectedValue(new Error("no network in test")),
      stats: vi.fn().mockRejectedValue(new Error("no network in test")),
      listSources: vi.fn(),
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

const row = (id: number, name: string) => ({
  id,
  name,
  consumers: 1,
  process: { state: "running" },
});

const frame = (sourceId: number, renditions: object[]) => ({
  data: JSON.stringify({
    type: "status",
    data: { source: { id: sourceId, name: `Studio ${sourceId}` }, destinations: [], renditions },
  }),
});

const sources = (...ids: number[]) =>
  ids.map((id) => ({ id, name: `Studio ${id}`, publishUrls: {} })) as unknown as Awaited<
    ReturnType<typeof api.listSources>
  >;

function Probe() {
  const { allRenditions, status, refreshSources } = useLiveData();
  return (
    <div>
      <span data-testid="all">{(allRenditions ?? []).map((r) => `${r.id}:${r.process ? "up" : "idle"}`).join(",")}</span>
      <span data-testid="own">{(status?.renditions ?? []).map((r) => r.id).join(",")}</span>
      <button onClick={() => void refreshSources()}>refresh</button>
    </div>
  );
}

beforeEach(() => {
  vi.stubGlobal("WebSocket", FakeWebSocket);
  vi.mocked(api.listSources).mockResolvedValue(sources(1, 2));
});

afterEach(() => {
  cleanup();
  FakeWebSocket.instances = [];
  vi.unstubAllGlobals();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

async function socket() {
  await waitFor(() => expect(FakeWebSocket.instances.length).toBeGreaterThan(0));
  return FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
}

describe("LiveDataProvider, status frames from more than one programme", () => {
  it("keeps every programme's renditions in allRenditions and the frame's own in status", async () => {
    render(
      <LiveDataProvider>
        <Probe />
      </LiveDataProvider>,
    );
    const ws = await socket();
    act(() => {
      ws.onmessage?.(frame(1, [row(10, "A ladder")]));
      ws.onmessage?.(frame(2, [row(20, "B ladder")]));
    });
    expect(screen.getByTestId("all").textContent).toBe("10:up,20:up");
    expect(screen.getByTestId("own").textContent, "status.renditions must stay the last frame's own").toBe("20");

    // One programme's own frame replaces ITS rows, including with none.
    act(() => {
      ws.onmessage?.(frame(1, []));
    });
    expect(screen.getByTestId("all").textContent).toBe("20:up");
  });

  it("drops a programme's renditions when the programme is removed, without a reload", async () => {
    render(
      <LiveDataProvider>
        <Probe />
      </LiveDataProvider>,
    );
    const ws = await socket();
    await waitFor(() => expect(api.listSources).toHaveBeenCalled());
    act(() => {
      ws.onmessage?.(frame(1, [row(10, "A ladder")]));
      ws.onmessage?.(frame(2, [row(20, "B ladder")]));
    });
    expect(screen.getByTestId("all").textContent).toBe("10:up,20:up");

    // Programme 2 is deleted; its engine never publishes again.
    vi.mocked(api.listSources).mockResolvedValue(sources(1));
    await act(async () => {
      screen.getByText("refresh").click();
    });
    await waitFor(() => expect(screen.getByTestId("all").textContent).toBe("10:up"));
  });

  it("folds the REST seed the same way, keeping the other programmes' rows", async () => {
    vi.mocked(api.status).mockResolvedValue({
      source: { id: 2, name: "Studio 2" },
      destinations: [],
      renditions: [row(20, "B ladder")],
    } as unknown as Awaited<ReturnType<typeof api.status>>);
    render(
      <LiveDataProvider>
        <Probe />
      </LiveDataProvider>,
    );
    const ws = await socket();
    act(() => ws.onmessage?.(frame(1, [row(10, "A ladder")])));
    await waitFor(() => expect(screen.getByTestId("all").textContent).toContain("20:up"));
    expect(screen.getByTestId("all").textContent).toBe("10:up,20:up");
  });

  it("drops a programme that stopped publishing while the others carry on", async () => {
    const now = vi.spyOn(Date, "now");
    now.mockReturnValue(1_000_000);
    render(
      <LiveDataProvider>
        <Probe />
      </LiveDataProvider>,
    );
    const ws = await socket();
    act(() => {
      ws.onmessage?.(frame(1, [row(10, "A ladder")]));
      ws.onmessage?.(frame(2, [row(20, "B ladder")]));
    });
    // Each engine publishes every 2 s. Programme 1 keeps going, 2 goes quiet.
    now.mockReturnValue(1_002_000);
    act(() => ws.onmessage?.(frame(1, [row(10, "A ladder")])));
    expect(screen.getByTestId("all").textContent).toBe("10:up,20:up");
    now.mockReturnValue(1_007_000);
    act(() => ws.onmessage?.(frame(1, [row(10, "A ladder")])));
    expect(screen.getByTestId("all").textContent).toBe("10:up");
  });
});

describe("Dashboard under the real provider, two programmes", () => {
  const originalFetch = globalThis.fetch;
  beforeEach(() => {
    if (!Element.prototype.scrollTo) Element.prototype.scrollTo = () => {};
    const routes: Record<string, unknown> = {
      "/api/v1/settings": { preview: { enabled: false }, recording: { enabled: false }, meters: { enabled: true }, failover: { enabled: false } },
      "/api/v1/system": { ingestUrl: "srt://box:6000", ffmpeg: { hasLibsrt: true } },
      "/api/v1/setup": { needsSetup: false, minPasswordChars: 8, sources: 2 },
      "/api/v1/sources": [{ id: 1, name: "Studio 1" }, { id: 2, name: "Studio 2" }],
      "/api/v1/metadata": { targets: [] },
      "/api/v1/metadata/broadcast-window": { accounts: [] },
    };
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
      const path = String(typeof input === "string" ? input : input.toString()).split("?")[0];
      return { ok: true, status: 200, text: async () => JSON.stringify(routes[path] ?? {}) } as unknown as Response;
    }) as unknown as typeof fetch;
  });
  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("lists only the selected programme's renditions on the Pipeline card", async () => {
    render(
      <MemoryRouter>
        <LiveDataProvider>
          <Dashboard />
        </LiveDataProvider>
      </MemoryRouter>,
    );
    const ws = await socket();
    await waitFor(() => expect(api.listSources).toHaveBeenCalled());
    act(() => {
      ws.onmessage?.(frame(1, [row(10, "Ladder of one")]));
      ws.onmessage?.(frame(2, [row(20, "Ladder of two")]));
      ws.onmessage?.(frame(1, [row(10, "Ladder of one")]));
    });
    const title = await screen.findByText(translate("en", "dash.pipeline"));
    const card = title.closest("[data-slot='card'], .rounded-xl, .rounded-lg") as HTMLElement;
    await waitFor(() => expect(card.textContent).toContain("Ladder of one"));
    expect(card.textContent, "the Pipeline card listed another programme's rendition").not.toContain("Ladder of two");
  });
});
