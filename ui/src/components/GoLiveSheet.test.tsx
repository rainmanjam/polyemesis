// @vitest-environment jsdom

/* THE GO LIVE SHEET REPLACES "START ALL".
 *
 * Start all fired every destination on the install at once, with no count and
 * no scope, and the outcomes arrived only at the end. The sheet is the whole
 * start laid out before anything fires: which destinations are ready, which
 * are blocked and why, how many YouTube broadcasts a Stop would end, and a
 * confirm that names the count. Progress is per row while it runs. */

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { FINAL_HOLD_MS, GoLiveSheet, SETTLE_TIMEOUT_MS } from "./GoLiveSheet";
import { goLiveRows } from "@/lib/goLive";
import { setLanguage, translate } from "@/lib/i18n";
import type { DestStatus, DestinationId, SourceView } from "@/lib/types";

afterEach(cleanup);

const T = (key: Parameters<typeof translate>[1], p?: Record<string, string | number>) =>
  translate("en", key, p);

const did = (n: number) => n as unknown as DestinationId;

/** A process that is on air: running, and its output has moved. "running"
 *  alone is a spawned FFmpeg that may yet be refused, and is not live. */
const onAir = { state: "running", startedAt: "2026-11-04T12:00:00Z", restarts: 0, progress: { outTimeMs: 4000 } };

const dest = (over: Record<string, unknown> = {}) =>
  ({
    id: did(1),
    name: "Twitch — no music",
    kind: "rtmp",
    platform: "twitch",
    enabled: false,
    summary: "",
    tracks: null,
    filterComplex: "",
    normalization: "off",
    warnings: null,
    sourceId: 1,
    ...over,
  }) as unknown as DestStatus;

const sources = [{ id: 1, name: "Studio A" }] as unknown as SourceView[];

function draw(
  destinations: DestStatus[],
  opts: { live?: DestStatus[]; onStart?: (id: DestinationId) => Promise<void>; paceMs?: number } = {},
) {
  const onStart = opts.onStart ?? vi.fn(async () => {});
  const rows = goLiveRows(destinations, sources);
  render(
    <GoLiveSheet
      open
      onOpenChange={() => {}}
      rows={rows}
      live={opts.live ?? destinations}
      youtubeAtRisk={(opts.live ?? destinations).filter((d) => d.enabled && d.platform === "youtube").length}
      onStart={onStart}
      onRetry={async () => {}}
      onFinished={() => {}}
      paceMs={opts.paceMs ?? 0}
    />,
  );
  return { onStart };
}

describe("GoLiveSheet: the whole start, before anything fires", () => {
  it("lists every destination with its readiness", () => {
    draw([dest({ id: did(1) }), dest({ id: did(2), name: "Archive", sourceId: 9 })]);

    expect(screen.getByText("Twitch — no music")).toBeTruthy();
    expect(screen.getByText(T("golive.ready"))).toBeTruthy();
    expect(screen.getByText(T("golive.blockedNoSource"))).toBeTruthy();
  });

  it("names the count it is about to start, in the one confirm button", () => {
    draw([dest({ id: did(1) }), dest({ id: did(2) }), dest({ id: did(3), sourceId: 9 })]);

    expect(screen.getByRole("button", { name: T("golive.goCount", { count: 2 }) })).toBeTruthy();
  });

  it("warns how many YouTube broadcasts a Stop would end, before any click", () => {
    draw([dest({ id: did(1), platform: "youtube", enabled: true })]);

    expect(screen.getByText(T("golive.youtubeWarnOne", { count: 1 }))).toBeTruthy();
  });

  it("says one broadcast, not one broadcasts, when a single YouTube destination is at risk", () => {
    draw([dest({ id: did(1), platform: "youtube", enabled: true })]);

    expect(screen.getByText("1 YouTube broadcast will end if stopped.")).toBeTruthy();
    expect(screen.queryByText("1 YouTube broadcasts will end if stopped.")).toBeNull();
  });

  it("says broadcasts when more than one YouTube destination is at risk", () => {
    draw([
      dest({ id: did(1), platform: "youtube", enabled: true }),
      dest({ id: did(2), name: "Second", platform: "youtube", enabled: true }),
    ]);

    expect(screen.getByText("2 YouTube broadcasts will end if stopped.")).toBeTruthy();
  });

  it("disables the confirm, and says why, when nothing is ready", () => {
    draw([dest({ id: did(1), sourceId: 9 })]);

    const go = screen.getByRole("button", { name: T("golive.goCount", { count: 0 }) });
    expect((go as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(T("golive.nothingReady"))).toBeTruthy();
  });
});

describe("GoLiveSheet: starting", () => {
  it("starts only the ready destinations that are not already on, in order", async () => {
    const started: number[] = [];
    const onStart = vi.fn(async (id: DestinationId) => {
      started.push(id as unknown as number);
    });
    draw(
      [dest({ id: did(1) }), dest({ id: did(2), sourceId: 9 }), dest({ id: did(3), enabled: true }), dest({ id: did(4) })],
      { onStart },
    );

    fireEvent.click(screen.getByRole("button", { name: T("golive.goCount", { count: 2 }) }));

    await waitFor(() => expect(started).toEqual([1, 4]));
    expect(started).not.toContain(2);
    expect(started).not.toContain(3);
  });

  it("does not start a destination that came on between opening the sheet and pressing Go live", async () => {
    // The rows are frozen at open. A destination that is on by the time of the
    // press must not be sent a second start: that is the flip the card's own
    // guard exists to prevent.
    const onStart = vi.fn(async (_id: DestinationId) => {});
    draw([dest({ id: did(1) }), dest({ id: did(2) })], {
      onStart,
      live: [dest({ id: did(1), enabled: true }), dest({ id: did(2) })],
    });

    // The count on the button is the count the press will act on: one, because
    // destination 1 is already on by the time the sheet is confirmed.
    fireEvent.click(screen.getByRole("button", { name: T("golive.goCount", { count: 1 }) }));

    await waitFor(() => expect(onStart).toHaveBeenCalledTimes(1));
    expect(onStart.mock.calls[0][0]).toBe(2);
  });

  it("shows a refused destination as failed with the reason, and carries on past it", async () => {
    const onStart = vi.fn(async (id: DestinationId) => {
      if ((id as unknown as number) === 1) throw new Error("ingest refused");
    });
    draw([dest({ id: did(1) }), dest({ id: did(2), name: "Second" })], { onStart });

    fireEvent.click(screen.getByRole("button", { name: T("golive.goCount", { count: 2 }) }));

    await screen.findByText("ingest refused");
    expect(onStart).toHaveBeenCalledTimes(2);
    expect(screen.getByText(T("golive.failed"))).toBeTruthy();
  });

  it("shows the progress of each row while it runs, from the live status", () => {
    draw([dest({ id: did(1) })], {
      live: [dest({ id: did(1), enabled: true, process: onAir })],
    });

    const row = screen.getByText("Twitch — no music").closest("[data-row]") as HTMLElement;
    expect(within(row).getByText(T("golive.live"))).toBeTruthy();
  });
});

describe("GoLiveSheet: what a press shows while it runs", () => {
  it("shows a refusal on its own row before the next destination is started", async () => {
    // Real timers and a real pace: the point is the ORDER of the two things, a
    // refusal visible while the walk is still waiting to reach the next row.
    const onStart = vi.fn(async (id: DestinationId) => {
      if ((id as unknown as number) === 1) throw new Error("ingest refused");
    });
    draw([dest({ id: did(1) }), dest({ id: did(2), name: "Second" })], { onStart, paceMs: 400 });

    fireEvent.click(screen.getByRole("button", { name: T("golive.goCount", { count: 2 }) }));

    await screen.findByText("ingest refused");
    expect(onStart).toHaveBeenCalledTimes(1);
  });

  it("forgets an earlier refusal when the sheet is opened again", async () => {
    const onStart = vi.fn(async () => {
      throw new Error("ingest refused");
    });
    const props = {
      open: true,
      onOpenChange: () => {},
      live: [dest({ id: did(1) })],
      youtubeAtRisk: 0,
      onStart,
      onRetry: async () => {},
      onFinished: () => {},
      paceMs: 0,
    };
    const { rerender } = render(<GoLiveSheet {...props} rows={goLiveRows([dest({ id: did(1) })], sources)} />);

    fireEvent.click(screen.getByRole("button", { name: T("golive.goCount", { count: 1 }) }));
    await screen.findByText("ingest refused");

    // The parent takes a fresh snapshot each time it opens.
    rerender(<GoLiveSheet {...props} rows={goLiveRows([dest({ id: did(1) })], sources)} />);
    expect(screen.queryByText("ingest refused")).toBeNull();
  });

  it("does not draw an enabled destination with no running process as live", () => {
    draw([dest({ id: did(1), enabled: true })], { live: [dest({ id: did(1), enabled: true })] });

    const row = screen.getByText("Twitch — no music").closest("[data-row]") as HTMLElement;
    expect(within(row).queryByText(T("golive.live"))).toBeNull();
  });
});

describe("Go live sheet: audio and scope (A3)", () => {
  const two = [{ id: 1, name: "Studio A" }, { id: 2, name: "Studio B" }] as unknown as SourceView[];

  function drawTwo(destinations: DestStatus[], onStart = vi.fn(async (_id: DestinationId) => {})) {
    render(
      <GoLiveSheet
        open
        onOpenChange={() => {}}
        rows={goLiveRows(destinations, two)}
        live={destinations}
        youtubeAtRisk={0}
        onStart={onStart}
        onRetry={async () => {}}
        onFinished={() => {}}
        paceMs={0}
      />,
    );
    return onStart;
  }

  it("shows each row's audio tracks", () => {
    drawTwo([dest({ tracks: [0, 2] })]);
    const row = document.querySelector("[data-row]") as HTMLElement;
    const on = within(row).getAllByTitle(/is included$/);
    expect(on.map((el) => el.textContent)).toEqual(["1", "3"]);
  });

  it("offers a filter per source when there is more than one, and starts only that source", async () => {
    const onStart = drawTwo([
      dest({ id: did(1), name: "A1", sourceId: 1 }),
      dest({ id: did(2), name: "B1", sourceId: 2 }),
      dest({ id: did(3), name: "B2", sourceId: 2 }),
    ]);
    fireEvent.click(screen.getByRole("button", { name: /Studio B/ }));
    expect(document.querySelectorAll("[data-row]").length).toBe(2);
    const go = screen.getByRole("button", { name: T("golive.goCount", { count: 2 }) });
    fireEvent.click(go);
    await waitFor(() => expect(onStart).toHaveBeenCalledTimes(2));
    expect(onStart.mock.calls.map((c) => c[0])).toEqual([did(2), did(3)]);
  });

  it("offers no filter for a single source", () => {
    drawTwo([dest({ sourceId: 1 })]);
    expect(screen.queryByRole("button", { name: /All sources/ })).toBeNull();
  });
});

describe("Go live sheet: a row that is already live says so (A8)", () => {
  it("draws a running destination as LIVE rather than READY, and counts only the rows it will start", () => {
    // The readiness badge used to read READY on a row that was already on air,
    // while the primary button counted it as something still to start. The row
    // now says what it is, and the count leaves it out.
    const running = dest({ id: did(1), enabled: true, process: onAir });
    const idle = dest({ id: did(2), name: "Archive", sourceId: 1 });
    draw([running, idle], { live: [running, idle] });

    const [firstRow] = Array.from(document.querySelectorAll("[data-row]")) as HTMLElement[];
    expect(within(firstRow).queryByText(T("golive.ready"))).toBeNull();
    expect(within(firstRow).getAllByText(T("golive.live")).length).toBeGreaterThan(0);
    expect(screen.getByRole("button", { name: T("golive.goCount", { count: "1" }) })).toBeTruthy();
  });
});

describe("Go live sheet: the source a destination uses is never cut off (A90)", () => {
  it("wraps a long source name and gives the full name as a tooltip, rather than truncating it", () => {
    // The source is the one thing an operator confirms before a press, and a
    // name cut to "Main Programme — St…" cannot be confirmed. It wraps now.
    const long = "Main Programme — Studio A, Front Room";
    const longSources = [{ id: 1, name: long }] as unknown as SourceView[];
    const rows = goLiveRows([dest({ id: did(1), enabled: false, sourceId: 1 })], longSources);
    render(
      <GoLiveSheet
        open
        onOpenChange={() => {}}
        rows={rows}
        live={[]}
        youtubeAtRisk={0}
        onStart={async () => {}}
        onRetry={async () => {}}
        onFinished={() => {}}
        paceMs={0}
      />,
    );
    const cell = screen.getByText(long);
    expect(cell.className).not.toContain("truncate");
    expect(cell.getAttribute("title")).toBe(long);
  });
});

describe("Go live sheet: the press counts what is actually live", () => {
  // The button used to count start REQUESTS, and the last one returns within a
  // frame, so "Starting 4 of 4" was never seen. It now counts, among the rows
  // this press set out to start, how many the live status says are on air;
  // failed rows count as settled and are named. The press is over when every
  // row has settled, the final count stands for FINAL_HOLD_MS, and a row stuck
  // in Starting gives the button back after SETTLE_TIMEOUT_MS.
  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  /** The timers set from here on, with a delay, that have neither fired nor been
   *  cleared. The dialog keeps timers of its own, so a global count is no use;
   *  this sees only what is set after the press. Call after useFakeTimers. */
  function watchTimers() {
    const pending = new Set<unknown>();
    const realSet = globalThis.setTimeout;
    const realClear = globalThis.clearTimeout;
    vi.spyOn(globalThis, "setTimeout").mockImplementation(((fn: (...a: unknown[]) => void, ms?: number, ...args: unknown[]) => {
      const id: unknown = realSet(() => {
        pending.delete(id);
        fn(...args);
      }, ms);
      if ((ms ?? 0) > 0) pending.add(id);
      return id;
    }) as unknown as typeof setTimeout);
    vi.spyOn(globalThis, "clearTimeout").mockImplementation(((id: unknown) => {
      pending.delete(id);
      realClear(id as Parameters<typeof clearTimeout>[0]);
    }) as unknown as typeof clearTimeout);
    return pending;
  }

  /** "spawned" is running with no media out yet; "stalled" is running, moved, then stuck. */
  type State = "running" | "spawned" | "stalled" | "starting" | "failed";
  const processFor = (s: State, run = 1) => {
    const base = { startedAt: `2026-11-04T12:00:0${run}Z`, restarts: run - 1 };
    if (s === "running") return { ...base, state: "running", progress: { outTimeMs: 4000 } };
    if (s === "spawned") return { ...base, state: "running", progress: { outTimeMs: 0 } };
    if (s === "stalled") return { ...base, state: "running", stalled: true, progress: { outTimeMs: 4000 } };
    return { ...base, state: s };
  };

  /** Three idle destinations; `set` re-renders with the live status given. */
  function press(opts: { n?: number; onStart?: (id: DestinationId) => Promise<void>; onFinished?: () => void } = {}) {
    const n = opts.n ?? 3;
    const idle = Array.from({ length: n }, (_, i) => dest({ id: did(i + 1), name: `Dest ${i + 1}` }));
    const props = {
      open: true,
      onOpenChange: () => {},
      rows: goLiveRows(idle, sources),
      youtubeAtRisk: 0,
      onStart: opts.onStart ?? vi.fn(async (_id: DestinationId) => {}),
      onRetry: async () => {},
      onFinished: opts.onFinished ?? (() => {}),
      paceMs: 0,
    };
    const utils = render(<GoLiveSheet {...props} live={idle} />);
    const set = (states: Record<number, State>) =>
      utils.rerender(
        <GoLiveSheet
          {...props}
          live={idle.map((d) => {
            const s = states[d.id as unknown as number];
            return s ? ({ ...d, enabled: true, process: processFor(s) } as unknown as DestStatus) : d;
          })}
        />,
      );
    const close = () => utils.rerender(<GoLiveSheet {...props} open={false} live={idle} />);
    return { ...utils, set, close, onStart: props.onStart };
  }

  const flush = () => act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  const wait = (ms: number) => act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
  const button = () => {
    const footer = screen.getByRole("button", { name: T("golive.cancel") }).parentElement as HTMLElement;
    return within(footer).getAllByRole("button").at(-1) as HTMLButtonElement;
  };
  const status = () => screen.getByTestId("golive-progress").textContent;

  it("counts up as the live status says each destination came on, on a disabled button", async () => {
    vi.useFakeTimers();
    const { set } = press();
    fireEvent.click(screen.getByRole("button", { name: "Go live · 3 ready" }));
    await flush();

    expect(button().textContent).toBe("Starting… 0 of 3 live");
    expect(button().disabled).toBe(true);
    set({ 1: "starting" });
    expect(button().textContent).toBe("Starting… 0 of 3 live");
    set({ 1: "running" });
    expect(button().textContent).toBe("Starting… 1 of 3 live");
    set({ 1: "running", 2: "running" });
    expect(button().textContent).toBe("Starting… 2 of 3 live");
    expect(button().disabled).toBe(true);
  });

  it("is not over when the last request returns, only when every row has settled", async () => {
    vi.useFakeTimers();
    const { set } = press();
    fireEvent.click(screen.getByRole("button", { name: "Go live · 3 ready" }));
    await flush();
    set({ 1: "running", 2: "running", 3: "starting" });
    await wait(5_000);
    expect(button().textContent).toBe("Starting… 2 of 3 live");
    expect(button().disabled).toBe(true);

    set({ 1: "running", 2: "running", 3: "running" });
    expect(button().textContent).toBe("3 of 3 live");
    expect(button().disabled).toBe(true);
  });

  it("holds the final count for about a second, still at 900 ms, gone at 1100 ms", async () => {
    vi.useFakeTimers();
    // The third is refused, so after the press it is ready again and the
    // button comes back enabled, which shows the hold is what disabled it.
    const onStart = vi.fn(async (id: DestinationId) => {
      if ((id as unknown as number) === 3) throw new Error("ingest refused");
    });
    const { set } = press({ onStart });
    fireEvent.click(screen.getByRole("button", { name: "Go live · 3 ready" }));
    await flush();
    set({ 1: "running", 2: "running" });

    expect(button().textContent).toBe("2 of 3 live · 1 failed");
    expect(button().disabled).toBe(true);
    await wait(900);
    expect(button().textContent).toBe("2 of 3 live · 1 failed");
    expect(button().disabled).toBe(true);
    await wait(200);
    expect(button().textContent).toBe("Go live · 1 ready");
    expect(button().disabled).toBe(false);
  });

  it("counts a destination whose process failed as settled, and names it", async () => {
    vi.useFakeTimers();
    const { set } = press();
    fireEvent.click(screen.getByRole("button", { name: "Go live · 3 ready" }));
    await flush();
    set({ 1: "running", 2: "failed", 3: "running" });

    expect(button().textContent).toBe("2 of 3 live · 1 failed");
  });

  it("holds the final count as it was, even if a row drops during the hold", async () => {
    vi.useFakeTimers();
    const onStart = vi.fn(async (id: DestinationId) => {
      if ((id as unknown as number) === 3) throw new Error("ingest refused");
    });
    const { set } = press({ onStart });
    fireEvent.click(screen.getByRole("button", { name: "Go live · 3 ready" }));
    await flush();
    set({ 1: "running", 2: "running" });
    expect(button().textContent).toBe("2 of 3 live · 1 failed");

    await wait(500);
    set({ 1: "running", 2: "starting" });
    expect(button().textContent).toBe("2 of 3 live · 1 failed");
    expect(status()).toBe("2 of 3 live · 1 failed");
    await wait(500);
    expect(button().textContent).toBe("Go live · 1 ready");
  });

  it("does not count a spawned process as live until its output has moved", async () => {
    vi.useFakeTimers();
    const { set } = press();
    fireEvent.click(screen.getByRole("button", { name: "Go live · 3 ready" }));
    await flush();
    set({ 1: "spawned", 2: "spawned", 3: "spawned" });

    expect(button().textContent).toBe("Starting… 0 of 3 live");
    const row = screen.getByText("Dest 1").closest("[data-row]") as HTMLElement;
    expect(within(row).queryByText(T("golive.live"))).toBeNull();
    set({ 1: "running", 2: "spawned", 3: "spawned" });
    expect(button().textContent).toBe("Starting… 1 of 3 live");
  });

  it("does not count a stalled process as live", async () => {
    vi.useFakeTimers();
    const { set } = press();
    fireEvent.click(screen.getByRole("button", { name: "Go live · 3 ready" }));
    await flush();
    set({ 1: "stalled", 2: "running", 3: "running" });

    expect(button().textContent).toBe("Starting… 2 of 3 live");
  });

  it("gives the button back after the settle timeout, with the real counts, when a row stays in Starting", async () => {
    vi.useFakeTimers();
    const onStart = vi.fn(async (id: DestinationId) => {
      if ((id as unknown as number) === 3) throw new Error("ingest refused");
    });
    const { set } = press({ onStart });
    fireEvent.click(screen.getByRole("button", { name: "Go live · 3 ready" }));
    await flush();
    set({ 1: "running", 2: "starting" });

    await wait(SETTLE_TIMEOUT_MS - 1);
    expect(button().textContent).toBe("Starting… 1 of 3 live · 1 failed");
    expect(button().disabled).toBe(true);
    await wait(1);
    expect(button().textContent).toBe("1 of 3 live · 1 failed · 1 still starting");
    await wait(FINAL_HOLD_MS);
    expect(button().textContent).toBe("Go live · 1 ready");
    expect(button().disabled).toBe(false);
  });

  it("announces the first word and the final count at once, and changes between at most every 2 s", async () => {
    vi.useFakeTimers();
    const { set } = press();
    fireEvent.click(screen.getByRole("button", { name: "Go live · 3 ready" }));
    await flush();
    const region = screen.getByTestId("golive-progress");
    expect(region.getAttribute("role")).toBe("status");
    expect(region.getAttribute("aria-live")).toBe("polite");
    expect(status()).toBe("Starting… 0 of 3 live");

    await wait(500);
    set({ 1: "running" });
    await flush();
    expect(status()).toBe("Starting… 0 of 3 live");
    await wait(500);
    set({ 1: "running", 2: "running" });
    await flush();
    expect(status()).toBe("Starting… 0 of 3 live");
    await wait(1_000);
    expect(status()).toBe("Starting… 2 of 3 live");

    await wait(100);
    set({ 1: "running", 2: "running", 3: "running" });
    await flush();
    expect(status()).toBe("3 of 3 live");
  });

  it("leaves no timer running and calls nothing back once unmounted mid-press", async () => {
    vi.useFakeTimers();
    let release: () => void = () => {};
    const onStart = vi.fn(
      (_id: DestinationId) =>
        new Promise<void>((resolve) => {
          release = resolve;
        }),
    );
    const onFinished = vi.fn();
    const { unmount } = press({ n: 1, onStart, onFinished });
    const pending = watchTimers();
    fireEvent.click(screen.getByRole("button", { name: "Go live · 1 ready" }));
    await flush();

    unmount();
    release();
    await flush();
    expect(onFinished).not.toHaveBeenCalled();
    expect(pending.size).toBe(0);
  });

  it("leaves no timer running once unmounted while waiting for rows to settle", async () => {
    vi.useFakeTimers();
    const { set, unmount } = press();
    const pending = watchTimers();
    fireEvent.click(screen.getByRole("button", { name: "Go live · 3 ready" }));
    await flush();
    set({ 1: "running" });
    // The settle timeout, and the announcement held back by the throttle.
    expect(pending.size).toBeGreaterThan(0);

    unmount();
    expect(pending.size).toBe(0);
  });

  it("leaves no timer running once unmounted while the final count is held", async () => {
    vi.useFakeTimers();
    const { set, unmount } = press();
    const pending = watchTimers();
    fireEvent.click(screen.getByRole("button", { name: "Go live · 3 ready" }));
    await flush();
    set({ 1: "running", 2: "running", 3: "running" });
    await wait(500);
    expect(button().textContent).toBe("3 of 3 live");
    expect(pending.size).toBeGreaterThan(0);

    unmount();
    expect(pending.size).toBe(0);
  });

  it("stops waiting, with no timer left, when the sheet is closed before the rows settle", async () => {
    vi.useFakeTimers();
    const { set, close } = press();
    const pending = watchTimers();
    fireEvent.click(screen.getByRole("button", { name: "Go live · 3 ready" }));
    await flush();
    set({ 1: "running" });
    expect(pending.size).toBeGreaterThan(0);

    close();
    await flush();
    expect(pending.size).toBe(0);
  });

  it("does not count a row as settled on a status left from before its start was sent", async () => {
    // The second destination is off, but its last process ended failed. That
    // status belongs to an earlier run and says nothing about this press: only
    // a status from a new process (a new startedAt) settles the row.
    vi.useFakeTimers();
    const one = dest({ id: did(1), name: "First" });
    const oldRun = { state: "failed", startedAt: "2026-11-03T09:00:00Z", restarts: 4 };
    const two = dest({ id: did(2), name: "Second", process: oldRun } as Record<string, unknown>);
    const props = {
      open: true,
      onOpenChange: () => {},
      rows: goLiveRows([one, two], sources),
      youtubeAtRisk: 0,
      onStart: vi.fn(async (_id: DestinationId) => {}),
      onRetry: async () => {},
      onFinished: () => {},
      paceMs: 0,
    };
    const { rerender } = render(<GoLiveSheet {...props} live={[one, two]} />);
    fireEvent.click(screen.getByRole("button", { name: "Go live · 2 ready" }));
    await flush();
    expect(props.onStart).toHaveBeenCalledTimes(2);

    const firstOn = { ...one, enabled: true, process: onAir } as unknown as DestStatus;
    rerender(<GoLiveSheet {...props} live={[firstOn, { ...two, enabled: true } as DestStatus]} />);
    await wait(3_000);
    expect(button().textContent).toBe("Starting… 1 of 2 live");
    expect(button().disabled).toBe(true);

    const newRunFailed = { state: "failed", startedAt: "2026-11-04T12:00:05Z", restarts: 0 };
    rerender(
      <GoLiveSheet {...props} live={[firstOn, { ...two, enabled: true, process: newRunFailed } as unknown as DestStatus]} />,
    );
    expect(button().textContent).toBe("1 of 2 live · 1 failed");
  });

  it("can be closed while it waits for rows to settle, but not while the starts go out", async () => {
    let release: () => void = () => {};
    const onStart = vi.fn(
      (_id: DestinationId) =>
        new Promise<void>((resolve) => {
          release = resolve;
        }),
    );
    press({ n: 1, onStart });
    fireEvent.click(screen.getByRole("button", { name: "Go live · 1 ready" }));
    await waitFor(() => expect(onStart).toHaveBeenCalled());
    expect((screen.getByRole("button", { name: T("golive.cancel") }) as HTMLButtonElement).disabled).toBe(true);

    release();
    await waitFor(() =>
      expect((screen.getByRole("button", { name: T("golive.cancel") }) as HTMLButtonElement).disabled).toBe(false),
    );
    expect(button().disabled).toBe(true);
  });
});

describe("Go live sheet: the confirm button reads one as one in every language", () => {
  // English "ready" does not change with the count, so these render in Spanish,
  // where it does: the button used to read "1 listos". Polish needed the count-
  // neutral colon form ("gotowe: 2"), since "{count} gotowych" was wrong at 1 to 4.
  afterEach(() => setLanguage("en"));

  it("says 1 listo, not 1 listos, with one destination ready", () => {
    setLanguage("es");
    draw([dest({ id: did(1) })]);

    expect(screen.getByRole("button", { name: "Salir en directo · 1 listo" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Salir en directo · 1 listos" })).toBeNull();
  });

  it("says 2 listos with two destinations ready", () => {
    setLanguage("es");
    draw([dest({ id: did(1) }), dest({ id: did(2), name: "Second" })]);

    expect(screen.getByRole("button", { name: "Salir en directo · 2 listos" })).toBeTruthy();
  });

  it("uses the count-neutral Polish form at 2", () => {
    setLanguage("pl");
    draw([dest({ id: did(1) }), dest({ id: did(2), name: "Second" })]);

    expect(screen.getByRole("button", { name: "Rozpocznij · gotowe: 2" })).toBeTruthy();
  });
});
