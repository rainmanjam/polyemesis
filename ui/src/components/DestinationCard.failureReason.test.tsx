// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { DestinationCard } from "./DestinationCard";
import type { DestStatus, FailureReason } from "@/lib/types";

/* #20 on the destination card: the classified reason replaces the raw FFmpeg
 * line as the sentence an operator reads, a held destination says so and
 * offers the one click that retries it, and a rotated key on a connected
 * account offers the one click that updates it. */
afterEach(cleanup);

const reason = (over: Partial<FailureReason> = {}): FailureReason => ({
  cause: "key_rejected",
  at: "2026-10-30T10:00:00Z",
  ...over,
});

const dest = (over: Partial<DestStatus> = {}): DestStatus =>
  ({
    id: 1,
    name: "YouTube main",
    kind: "rtmp",
    platform: "youtube",
    enabled: true,
    summary: "",
    tracks: null,
    filterComplex: "",
    normalization: "off",
    warnings: null,
    process: {
      name: "dest:1",
      kind: "destination",
      state: "failed",
      pid: 0,
      restarts: 2,
      uptimeSec: 0,
      lastError: "exit status 251: Error opening output rtmp://a.rtmp.youtube.com/live2/****: I/O error",
      progress: {},
    },
    ...over,
  }) as DestStatus;

function draw(over: Partial<DestStatus>, handlers: { onRestart?: () => void; onRefreshKey?: () => void } = {}) {
  const noop = () => {};
  return render(
    <MemoryRouter>
      <DestinationCard
        dest={dest(over)}
        onStart={noop}
        onStop={noop}
        onRestart={handlers.onRestart ?? noop}
        onEdit={noop}
        onDelete={noop}
        onRefreshKey={handlers.onRefreshKey ?? noop}
        onMoveEarlier={noop}
        onMoveLater={noop}
        canMoveEarlier={false}
        canMoveLater={false}
      />
    </MemoryRouter>,
  );
}

describe("the destination card names why a destination is failing", () => {
  it("shows the classified cause in place of FFmpeg's raw last line", () => {
    draw({ reason: reason() });
    const row = screen.getByTestId("card-failure-reason");
    expect(row.textContent).toContain("Stream key rejected");
    // The step and the as-of are still the B1 facts beside the reason.
    expect(row.textContent).toContain("Output to YouTube");
    expect(screen.queryByText(/exit status 251/)).toBeNull();
  });

  it("an unknown cause keeps FFmpeg's own words, so there is still something to search for", () => {
    draw({ reason: reason({ cause: "unknown", line: "Error closing file: Broken pipe" }) });
    const row = screen.getByTestId("card-failure-reason");
    expect(row.textContent).toContain("Unknown failure");
    expect(row.textContent).toContain("Error closing file: Broken pipe");
  });

  it("a held destination says retrying has stopped and offers one click to retry", () => {
    const onRestart = vi.fn();
    draw({ reason: reason({ held: true, rejections: 3 }) }, { onRestart });
    expect(screen.getByText(/Held after 3 quick rejections/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    expect(onRestart).toHaveBeenCalledTimes(1);
  });

  it("a destination that is not held offers no Retry button: it is already retrying", () => {
    draw({ reason: reason() });
    expect(screen.queryByRole("button", { name: /^Retry/ })).toBeNull();
  });

  it("a rotated key on the connected account offers the update as one click", () => {
    const onRefreshKey = vi.fn();
    draw({ reason: reason({ keyChanged: true }) }, { onRefreshKey });
    fireEvent.click(screen.getByRole("button", { name: /YouTube's key changed — click to update/ }));
    expect(onRefreshKey).toHaveBeenCalledTimes(1);
  });

  it("a slow upload is named while the destination is still running", () => {
    draw({
      reason: reason({ cause: "upload_too_slow" }),
      process: { ...dest().process!, state: "running", uptimeSec: 60, lastError: undefined },
    });
    expect(screen.getByTestId("card-failure-reason").textContent).toContain("Upload too slow");
  });

  it("with no classified reason the card falls back to the raw last error, as before", () => {
    draw({});
    expect(screen.queryByTestId("card-failure-reason")).toBeNull();
    expect(screen.getByText(/exit status 251/)).toBeTruthy();
  });
});
