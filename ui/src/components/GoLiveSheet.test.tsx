// @vitest-environment jsdom

/* THE GO LIVE SHEET REPLACES "START ALL".
 *
 * Start all fired every destination on the install at once, with no count and
 * no scope, and the outcomes arrived only at the end. The sheet is the whole
 * start laid out before anything fires: which destinations are ready, which
 * are blocked and why, how many YouTube broadcasts a Stop would end, and a
 * confirm that names the count. Progress is per row while it runs. */

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { GoLiveSheet } from "./GoLiveSheet";
import { goLiveRows } from "@/lib/goLive";
import { setLanguage, translate } from "@/lib/i18n";
import type { DestStatus, DestinationId, SourceView } from "@/lib/types";

afterEach(cleanup);

const T = (key: Parameters<typeof translate>[1], p?: Record<string, string | number>) =>
  translate("en", key, p);

const did = (n: number) => n as unknown as DestinationId;

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
      live: [dest({ id: did(1), enabled: true, process: { state: "running" } })],
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
    const running = dest({ id: did(1), enabled: true, process: { state: "running" } });
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

describe("Go live sheet: one press reads as one press while it runs", () => {
  // The press starts destinations one at a time. The button used to recount
  // "Go live · 3 ready" downward as each came on, so one click looked as if it
  // had started one destination. It now counts up through the press it made.
  function deferredStarts() {
    const pending = new Map<number, { resolve: () => void; reject: (e: Error) => void }>();
    const onStart = vi.fn(
      (id: DestinationId) =>
        new Promise<void>((resolve, reject) => {
          pending.set(id as unknown as number, { resolve, reject });
        }),
    );
    const settle = async (id: number, err?: Error) => {
      await waitFor(() => expect(pending.has(id)).toBe(true));
      const p = pending.get(id)!;
      if (err) p.reject(err);
      else p.resolve();
    };
    return { onStart, settle };
  }

  const three = () => [dest({ id: did(1) }), dest({ id: did(2), name: "Second" }), dest({ id: did(3), name: "Third" })];

  it("shows Starting 1 of 3 on a disabled button, and announces it, while the first start is in flight", async () => {
    const { onStart, settle } = deferredStarts();
    draw(three(), { onStart });

    fireEvent.click(screen.getByRole("button", { name: "Go live · 3 ready" }));

    const button = await screen.findByRole("button", { name: "Starting 1 of 3…" });
    expect((button as HTMLButtonElement).disabled).toBe(true);
    const status = screen.getByTestId("golive-progress");
    expect(status.getAttribute("role")).toBe("status");
    expect(status.getAttribute("aria-live")).toBe("polite");
    expect(status.textContent).toBe("Starting 1 of 3…");

    await settle(1);
    expect(await screen.findByRole("button", { name: "Starting 2 of 3…" })).toBeTruthy();
    await settle(2);
    await settle(3);
    await screen.findByRole("button", { name: "Go live · 3 ready" });
  });

  it("returns to the normal, enabled button once every start is done", async () => {
    const { onStart, settle } = deferredStarts();
    draw(three(), { onStart });

    fireEvent.click(screen.getByRole("button", { name: "Go live · 3 ready" }));
    await settle(1);
    await settle(2);
    await settle(3);

    const button = await screen.findByRole("button", { name: "Go live · 3 ready" });
    expect((button as HTMLButtonElement).disabled).toBe(false);
    expect(screen.queryByRole("button", { name: /^Starting/ })).toBeNull();
    expect(screen.getByTestId("golive-progress").textContent).toBe("");
  });

  it("keeps counting, and does not say nothing is ready, as the live list catches up mid-run", async () => {
    // In the console the live list updates as each start lands, so readyNow
    // empties before the walk ends. Neither the button nor the empty-state line
    // may follow it while the press is still running.
    const { onStart, settle } = deferredStarts();
    const idle = three();
    const props = {
      open: true,
      onOpenChange: () => {},
      rows: goLiveRows(idle, sources),
      youtubeAtRisk: 0,
      onStart,
      onRetry: async () => {},
      onFinished: () => {},
      paceMs: 0,
    };
    const { rerender } = render(<GoLiveSheet {...props} live={idle} />);

    fireEvent.click(screen.getByRole("button", { name: "Go live · 3 ready" }));
    await settle(1);
    await settle(2);
    await screen.findByRole("button", { name: "Starting 3 of 3…" });
    rerender(<GoLiveSheet {...props} live={idle.map((d) => ({ ...d, enabled: true }))} />);

    expect(screen.getByRole("button", { name: "Starting 3 of 3…" })).toBeTruthy();
    expect(screen.queryByText(T("golive.nothingReady"))).toBeNull();
    await settle(3);
    await screen.findByRole("button", { name: T("golive.goCount", { count: 0 }) });
  });

  it("still counts through to the end when a start fails mid-run, and shows the failed row", async () => {
    const { onStart, settle } = deferredStarts();
    draw(three(), { onStart });

    fireEvent.click(screen.getByRole("button", { name: "Go live · 3 ready" }));
    await settle(1);
    await screen.findByRole("button", { name: "Starting 2 of 3…" });
    await settle(2, new Error("ingest refused"));

    // The refusal does not stall or reset the count: the walk goes on to the third.
    const third = await screen.findByRole("button", { name: "Starting 3 of 3…" });
    expect((third as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("ingest refused")).toBeTruthy();
    await settle(3);

    const button = await screen.findByRole("button", { name: "Go live · 3 ready" });
    expect((button as HTMLButtonElement).disabled).toBe(false);
    expect(onStart).toHaveBeenCalledTimes(3);
    const row = screen.getByText("Second").closest("[data-row]") as HTMLElement;
    expect(within(row).getByText(T("golive.failed"))).toBeTruthy();
    expect(within(row).getByText("ingest refused")).toBeTruthy();
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
