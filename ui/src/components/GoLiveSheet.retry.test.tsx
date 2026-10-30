// @vitest-environment jsdom

/* A3: a failed row in the Go live sheet retries in one click, where the
 * failure is shown, and says WHY it failed in the classified words of #20.
 *
 * Two kinds of failure reach a row, and they need different doors. A start
 * request the server refused never made a process, so retrying it is the
 * start again. A process that failed -- held after quick rejections, or out of
 * restarts -- is only revived by a restart; a start on an enabled destination
 * is a no-op. Pressing Retry must take the door that actually does something. */

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { GoLiveSheet } from "./GoLiveSheet";
import { goLiveRows } from "@/lib/goLive";
import type { DestStatus, DestinationId, SourceView } from "@/lib/types";

afterEach(cleanup);

const did = (n: number) => n as unknown as DestinationId;
const sources = [{ id: 1, name: "Studio A" }] as unknown as SourceView[];

const dest = (over: Record<string, unknown> = {}) =>
  ({
    id: did(1),
    name: "YouTube main",
    kind: "rtmp",
    platform: "youtube",
    enabled: false,
    summary: "",
    tracks: null,
    filterComplex: "",
    normalization: "off",
    warnings: null,
    sourceId: 1,
    ...over,
  }) as unknown as DestStatus;

function draw(snapshot: DestStatus[], live: DestStatus[], handlers: {
  onStart?: (id: DestinationId) => Promise<void>;
  onRetry?: (id: DestinationId) => Promise<void>;
}) {
  const onStart = handlers.onStart ?? vi.fn(async () => {});
  const onRetry = handlers.onRetry ?? vi.fn(async () => {});
  render(
    <GoLiveSheet
      open
      onOpenChange={() => {}}
      rows={goLiveRows(snapshot, sources)}
      live={live}
      youtubeAtRisk={0}
      onStart={onStart}
      onRetry={onRetry}
      onFinished={() => {}}
      paceMs={0}
    />,
  );
  return { onStart, onRetry };
}

describe("GoLiveSheet: Retry on a failed row", () => {
  it("a held destination's row names the cause and retries by restarting it", async () => {
    const failed = dest({
      enabled: true,
      process: { state: "failed", restarts: 2 },
      reason: { cause: "key_rejected", held: true, rejections: 3, at: "2026-10-30T10:00:00Z" },
    });
    const { onRetry, onStart } = draw([dest()], [failed], {});
    expect(screen.getByText("Stream key rejected")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry YouTube main" }));
    await waitFor(() => expect(onRetry).toHaveBeenCalledWith(did(1)));
    expect(onStart).not.toHaveBeenCalled();
  });

  it("a start the server refused retries with a start, because there is no process to restart", async () => {
    let calls = 0;
    const onStart = vi.fn(async () => {
      calls++;
      if (calls === 1) throw new Error("the source this destination carries is not running");
    });
    const { onRetry } = draw([dest()], [dest()], { onStart });
    fireEvent.click(screen.getByRole("button", { name: /Go live/ }));
    await waitFor(() => expect(screen.getByText(/not running/)).toBeTruthy());
    fireEvent.click(screen.getByRole("button", { name: "Retry YouTube main" }));
    await waitFor(() => expect(onStart).toHaveBeenCalledTimes(2));
    expect(onRetry).not.toHaveBeenCalled();
  });

  it("a row that is not failed offers no Retry", () => {
    draw([dest()], [dest({ enabled: true, process: { state: "running" } })], {});
    expect(screen.queryByRole("button", { name: /^Retry/ })).toBeNull();
  });
});
