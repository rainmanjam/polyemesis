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
import { translate } from "@/lib/i18n";
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

    expect(screen.getByText(T("golive.youtubeWarn", { count: 1 }))).toBeTruthy();
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
