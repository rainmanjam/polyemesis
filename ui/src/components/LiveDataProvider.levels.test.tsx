// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";

import { LiveDataProvider } from "./LiveDataProvider";
import { useLiveData } from "@/hooks/useLiveData";
import { api } from "@/lib/api";

/* THE METERS FOLLOW THE PROGRAMME IN THE HEADER.
 *
 * The socket is install-wide: every engine publishes its levels onto it. The
 * provider used to replace `levels` with every frame, so on a multi-programme
 * install the meters drew whichever programme metered last. On the
 * three-programme demo, tracks 3 and 4 of a four-track programme read "not
 * measured" in about 40% of samples -- a two-track programme's frame, drawn
 * over the selected one. The same bug #924 fixed for status: a frame that
 * names another programme is not this programme's reading.
 */

vi.spyOn(api, "listSources").mockResolvedValue([
  { id: 1, name: "Main" },
  { id: 2, name: "Studio B" },
] as never);
vi.spyOn(api, "source").mockResolvedValue(null as never);
vi.spyOn(api, "status").mockRejectedValue(new Error("no network in test"));
vi.spyOn(api, "stats").mockRejectedValue(new Error("no network in test"));

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

/** Every (programme, tracks) pair any render saw, so a one-render leak --
 *  the new programme's name over the old programme's bars -- is caught even
 *  though act() flushes the effect that would clear it. */
const renders: string[] = [];

function Probe() {
  const { levels, programme, selectProgramme } = useLiveData();
  renders.push(`${programme}:${levels ? (levels.peak?.length ?? 0) : "none"}`);
  return (
    <div>
      <span data-testid="programme">{String(programme)}</span>
      <span data-testid="tracks">{levels ? String(levels.peak?.length ?? 0) : "none"}</span>
      <button onClick={() => selectProgramme(2)}>pick 2</button>
    </div>
  );
}

/** A levels frame with `tracks` stereo tracks, tagged for `id` (or untagged). */
function frame(tracks: number, id?: number) {
  const ch = () => Array.from({ length: tracks }, () => [-12, -12]);
  const data: Record<string, unknown> = { peak: ch(), rms: ch() };
  if (id !== undefined) data.source = { id, name: `s${id}` };
  return { data: JSON.stringify({ type: "levels", data }) };
}

async function openOnProgrammeOne() {
  render(
    <LiveDataProvider>
      <Probe />
    </LiveDataProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("programme").textContent).toBe("1"));
  await waitFor(() => expect(FakeWebSocket.instances.length).toBeGreaterThan(0));
  const ws = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
  act(() => ws.onopen?.());
  return ws;
}

beforeEach(() => {
  vi.stubGlobal("WebSocket", FakeWebSocket);
  try {
    localStorage.clear();
  } catch {
    /* private mode */
  }
});

afterEach(() => {
  cleanup();
  renders.length = 0;
  FakeWebSocket.instances = [];
  vi.unstubAllGlobals();
});

describe("LiveDataProvider: levels on a multi-programme install", () => {
  it("keeps the selected programme's levels when another programme's frame arrives", async () => {
    const ws = await openOnProgrammeOne();

    act(() => ws.onmessage?.(frame(4, 1)));
    expect(screen.getByTestId("tracks").textContent).toBe("4");

    // Programme 2 meters two tracks. Drawn over programme 1, tracks 3 and 4
    // would read "not measured".
    act(() => ws.onmessage?.(frame(2, 2)));
    expect(screen.getByTestId("tracks").textContent).toBe("4");

    act(() => ws.onmessage?.(frame(3, 1)));
    expect(screen.getByTestId("tracks").textContent).toBe("3");
  });

  it("drops the old programme's reading when the operator switches", async () => {
    const ws = await openOnProgrammeOne();
    act(() => ws.onmessage?.(frame(4, 1)));
    expect(screen.getByTestId("tracks").textContent).toBe("4");

    await act(async () => {
      screen.getByText("pick 2").click();
    });
    expect(screen.getByTestId("programme").textContent).toBe("2");
    // Programme 1's bars, under programme 2's name, until programme 2 speaks.
    expect(screen.getByTestId("tracks").textContent).toBe("none");
    // Not even for the one render between the switch and its effect.
    expect(renders).not.toContain("2:4");

    // A frame already queued on the old socket lands after the switch. Its
    // handler still holds programme 1, so unguarded it would put programme 1's
    // bars back under programme 2's name.
    act(() => ws.onmessage?.(frame(4, 1)));
    expect(screen.getByTestId("tracks").textContent).toBe("none");
    // An untagged one cannot be told apart by its tag at all; only the torn-down
    // socket's own guard stops it.
    act(() => ws.onmessage?.(frame(4)));
    expect(screen.getByTestId("tracks").textContent).toBe("none");
  });

  it("drops an untagged reading when the operator switches, too", async () => {
    // A server that predates the tag: the frame cannot be scoped by its tag,
    // so the teardown of the old programme's socket is what clears it.
    const ws = await openOnProgrammeOne();
    act(() => ws.onmessage?.(frame(4)));
    expect(screen.getByTestId("tracks").textContent).toBe("4");

    await act(async () => {
      screen.getByText("pick 2").click();
    });
    expect(screen.getByTestId("programme").textContent).toBe("2");
    expect(screen.getByTestId("tracks").textContent).toBe("none");
  });

  it("still applies an untagged frame, from a server that predates the tag", async () => {
    const ws = await openOnProgrammeOne();
    act(() => ws.onmessage?.(frame(2)));
    expect(screen.getByTestId("tracks").textContent).toBe("2");
  });
});
