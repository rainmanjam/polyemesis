// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { useLiveData } from "@/hooks/useLiveData";
import { LiveDataProvider } from "./LiveDataProvider";
import { api } from "@/lib/api";

/* THE ANSWER WAS RESOLVED ONCE AND THE QUESTION KEPT CHANGING.
 *
 * listSources() ran in an effect with [] deps and nothing re-ran it. Create a
 * second source during setup, or delete the one being followed, and the whole
 * console stayed pointed at a stale programme: Meters "NOT UPDATING",
 * Monitoring's process list dead, Clips "No clips yet." -- against a healthy
 * server, until a reload. Each of those is a plausible idle state, which is
 * why it was never reported.
 *
 * So the assertions are about RE-resolution, and one control asserts a
 * refresh does NOT move an operator off a programme that still exists --
 * re-resolving from scratch every time would drag a two-programme install
 * back to source[0] whenever any source was touched. #646.
 */

const list = vi.spyOn(api, "listSources");
const srcOf = vi.spyOn(api, "source");

function Probe() {
  const { programme, sourceCount, sourceCountKnown, refreshSources, programmeKnown } = useLiveData();
  return (
    <div>
      <span data-testid="programme">{String(programme)}</span>
      <span data-testid="count">{sourceCount}</span>
      <span data-testid="known">{String(programmeKnown)}</span>
      <span data-testid="countKnown">{String(sourceCountKnown)}</span>
      <button onClick={() => void refreshSources()}>refresh</button>
    </div>
  );
}

const rows = (...ids: number[]) => ids.map((id) => ({ id, name: `s${id}` })) as never;

function draw() {
  return render(
    <LiveDataProvider>
      <Probe />
    </LiveDataProvider>,
  );
}

beforeEach(() => {
  list.mockReset();
  srcOf.mockReset();
  srcOf.mockResolvedValue(null as never);
  try {
    localStorage.clear();
  } catch {
    /* private mode */
  }
});
afterEach(cleanup);

describe("LiveDataProvider: programme resolution", () => {
  it("picks up a source created after the first render", async () => {
    list.mockResolvedValueOnce(rows(1));
    draw();
    await waitFor(() => expect(screen.getByTestId("count").textContent).toBe("1"));

    // The second source appears; the page asks for a refresh.
    list.mockResolvedValueOnce(rows(1, 2));
    await act(async () => {
      screen.getByText("refresh").click();
    });
    await waitFor(() => expect(screen.getByTestId("count").textContent).toBe("2"));
  });

  it("moves off a programme that has been deleted", async () => {
    list.mockResolvedValueOnce(rows(7, 8));
    draw();
    await waitFor(() => expect(screen.getByTestId("programme").textContent).toBe("7"));

    list.mockResolvedValueOnce(rows(8));
    await act(async () => {
      screen.getByText("refresh").click();
    });
    // Not merely "changed" -- it must land on the source that still exists.
    await waitFor(() => expect(screen.getByTestId("programme").textContent).toBe("8"));
  });

  it("keeps the operator on their programme when it still exists", async () => {
    // The control. Without it, this file would pass just as happily on a
    // provider that re-resolved to available[0] on every refresh, which would
    // drag a two-programme install back to the first source constantly.
    list.mockResolvedValueOnce(rows(7, 8));
    draw();
    await waitFor(() => expect(screen.getByTestId("programme").textContent).toBe("7"));

    list.mockResolvedValueOnce(rows(8, 7));
    await act(async () => {
      screen.getByText("refresh").click();
    });
    await waitFor(() => expect(screen.getByTestId("count").textContent).toBe("2"));
    expect(screen.getByTestId("programme").textContent).toBe("7");
  });

  it("still answers when /sources cannot be read", async () => {
    // A console that will not render because it could not list sources is
    // worse than one showing the install's only programme.
    list.mockRejectedValueOnce(new Error("unreachable"));
    draw();
    await waitFor(() => expect(screen.getByTestId("known").textContent).toBe("true"));
    expect(screen.getByTestId("programme").textContent).toBe("null");
  });
});

describe("LiveDataProvider: self-healing from the socket", () => {
  it("re-reads /sources when a snapshot names a programme it has never seen", async () => {
    // The changes this tab did not make: a source created in another tab, by
    // another operator, or straight through the API never passes through
    // refreshSources(). The status feed is install-wide and every snapshot
    // names its programme, so one naming an id we do not hold is proof the
    // list is stale. #646.
    list.mockResolvedValue(rows(1));
    srcOf.mockResolvedValue({ id: 99, name: "made elsewhere", probed: true, tracks: null } as never);

    draw();
    await waitFor(() => expect(screen.getByTestId("known").textContent).toBe("true"));

    // One call to establish the list, at least one more once the snapshot
    // arrives naming a programme outside it.
    await waitFor(() => expect(list.mock.calls.length).toBeGreaterThan(1), { timeout: 3000 });
  });

  it("does not re-read for a snapshot about the programme it is already on", async () => {
    // The control. Without it this would pass on a provider that re-read
    // /sources on every snapshot, which is a request per poll forever.
    list.mockResolvedValue(rows(1));
    srcOf.mockResolvedValue({ id: 1, name: "Main", probed: true, tracks: null } as never);

    draw();
    await waitFor(() => expect(screen.getByTestId("known").textContent).toBe("true"));
    const settled = list.mock.calls.length;
    await new Promise((r) => setTimeout(r, 300));
    expect(list.mock.calls.length).toBe(settled);
  });
});

describe("LiveDataProvider: the source count stays true with no socket frame", () => {
  it("notices the last source going away, because an engine-less install sends no status event", async () => {
    // Status events are published BY an engine, so an install that loses its
    // last source goes quiet and the last frame stays on screen. The Dashboard
    // used to run its own 10 s poll of the count to cover this; the provider
    // owns the count, so it owns the poll.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      list.mockResolvedValue(rows(1));
      draw();
      await waitFor(() => expect(screen.getByTestId("count").textContent).toBe("1"));

      list.mockResolvedValue(rows());
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10_000);
      });
      await waitFor(() => expect(screen.getByTestId("count").textContent).toBe("0"));
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("LiveDataProvider: a poll that fails or answers late", () => {
  it("keeps the last good programme and list when a later read fails", async () => {
    list.mockResolvedValueOnce(rows(7, 8));
    draw();
    await waitFor(() => expect(screen.getByTestId("programme").textContent).toBe("7"));

    list.mockRejectedValueOnce(new Error("unreachable"));
    await act(async () => {
      screen.getByText("refresh").click();
    });
    // Dropping to null would unscope the socket and every REST read.
    expect(screen.getByTestId("programme").textContent).toBe("7");
    expect(screen.getByTestId("count").textContent).toBe("2");
  });

  it("lets only the newest read apply, whatever order the answers arrive in", async () => {
    list.mockResolvedValueOnce(rows(7, 8));
    draw();
    await waitFor(() => expect(screen.getByTestId("count").textContent).toBe("2"));

    let older!: (v: never) => void;
    list.mockImplementationOnce(() => new Promise((r) => (older = r as never)));
    list.mockResolvedValueOnce(rows(8));
    await act(async () => {
      screen.getByText("refresh").click(); // older request, still in flight
      screen.getByText("refresh").click(); // newer request, answers at once
    });
    await waitFor(() => expect(screen.getByTestId("count").textContent).toBe("1"));
    // The older answer (which still lists 7) lands last and must be dropped.
    await act(async () => older(rows(7, 8)));
    expect(screen.getByTestId("count").textContent).toBe("1");
    expect(screen.getByTestId("programme").textContent).toBe("8");
  });
});

describe("LiveDataProvider: the count is unknown until /sources answers", () => {
  it("does not report a failed first read as zero programmes", async () => {
    list.mockRejectedValueOnce(new Error("unreachable"));
    draw();
    await waitFor(() => expect(screen.getByTestId("known").textContent).toBe("true"));
    expect(screen.getByTestId("countKnown").textContent).toBe("false");
  });

  it("never turns a known count back into unknown when a later read fails", async () => {
    list.mockResolvedValueOnce(rows(7, 8));
    draw();
    await waitFor(() => expect(screen.getByTestId("countKnown").textContent).toBe("true"));
    list.mockRejectedValueOnce(new Error("unreachable"));
    await act(async () => {
      screen.getByText("refresh").click();
    });
    expect(screen.getByTestId("countKnown").textContent).toBe("true");
    expect(screen.getByTestId("count").textContent).toBe("2");
  });
});

describe("LiveDataProvider: the poll pauses in a hidden tab", () => {
  const setHidden = (hidden: boolean) =>
    Object.defineProperty(document, "hidden", { value: hidden, configurable: true });
  afterEach(() => setHidden(false));

  it("does not poll while hidden, and reads at once on coming back", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      list.mockResolvedValue(rows(1));
      draw();
      await waitFor(() => expect(screen.getByTestId("count").textContent).toBe("1"));
      const before = list.mock.calls.length;

      setHidden(true);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(list.mock.calls.length).toBe(before);

      setHidden(false);
      await act(async () => {
        document.dispatchEvent(new Event("visibilitychange"));
      });
      await waitFor(() => expect(list.mock.calls.length).toBe(before + 1));
    } finally {
      vi.useRealTimers();
    }
  });
});
