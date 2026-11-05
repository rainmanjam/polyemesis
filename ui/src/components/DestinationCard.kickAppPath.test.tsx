// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { DestinationCard } from "./DestinationCard";
import type { DestStatus, FailureReason, ProcessStatus } from "@/lib/types";

/* The field report this file is named for (v0.14.0): a Kick destination saved
 * with the URL Kick's dashboard prints -- an Amazon IVS ingest with no /app --
 * drew a red LIVE badge at 0 kbps with "--:--:--" uptime, above a box saying
 * "Secure (TLS) connection failed" and advising a look at ports and
 * certificates. Neither half was true: nothing was on air, and the URL was the
 * thing to change. */
afterEach(cleanup);

const fix = "rtmps://fa723fc1b171.global-contribute.live-video.net:443/app";

const proc = (over: Partial<ProcessStatus> = {}): ProcessStatus =>
  ({
    name: "dest:7",
    kind: "destination",
    state: "running",
    pid: 4242,
    restarts: 1,
    uptimeSec: 0,
    progress: { bitrateKbps: 0 },
    ...over,
  }) as ProcessStatus;

const dest = (over: Partial<DestStatus> = {}): DestStatus =>
  ({
    id: 7,
    name: "Kick",
    kind: "rtmp",
    platform: "kick",
    enabled: true,
    summary: "",
    tracks: null,
    filterComplex: "",
    normalization: "off",
    warnings: null,
    process: proc(),
    ...over,
  }) as DestStatus;

function draw(over: Partial<DestStatus>, onEdit: () => void = () => {}) {
  const noop = () => {};
  return render(
    <MemoryRouter>
      <DestinationCard
        dest={dest(over)}
        onStart={noop}
        onStop={noop}
        onRestart={noop}
        onEdit={onEdit}
        onDelete={noop}
        onRefreshKey={noop}
        onMoveEarlier={noop}
        onMoveLater={noop}
        canMoveEarlier={false}
        canMoveLater={false}
      />
    </MemoryRouter>,
  );
}

describe("a destination whose process runs but has carried no media", () => {
  // Mutation: make destinationProcessState return process.state unchanged.
  // Observed: FAIL, the badge reads Live with the on-air cue.
  it("is not drawn on air: Starting, amber, no tally lamp", () => {
    draw({ process: proc({ uptimeSec: 0 }) });
    const badge = screen.getByTestId("card-state-badge");
    expect(badge.textContent).toBe("Starting");
    expect(badge.querySelector('[data-badge-cue="on-air"]')).toBeNull();
    expect(badge.className).not.toContain("bg-tally");
  });

  it("is on air once this run has carried media", () => {
    draw({ process: proc({ uptimeSec: 12, progress: { bitrateKbps: 6000 } as ProcessStatus["progress"] }) });
    const badge = screen.getByTestId("card-state-badge");
    expect(badge.textContent).toBe("Live");
    expect(badge.querySelector('[data-badge-cue="on-air"]')).not.toBeNull();
  });
});

describe("a failure the destination's URL explains", () => {
  const reason = (over: Partial<FailureReason> = {}): FailureReason => ({
    cause: "key_rejected",
    at: "2026-11-05T10:00:00Z",
    quick: true,
    ...over,
  });

  // Mutation: drop the reason.urlFix branches in DestinationCard. Observed:
  // FAIL, the box says "Stream key rejected" and offers the generic remedy.
  it("names the missing /app and the URL that works, not the cause FFmpeg's output suggested", () => {
    const onEdit = vi.fn();
    draw({ reason: reason({ urlFix: fix }) }, onEdit);
    const box = screen.getByTestId("card-failure-reason");
    expect(box.textContent).toContain("Server URL is missing /app");
    expect(box.textContent).toContain(fix);
    expect(box.textContent).not.toContain("Stream key rejected");
    expect(box.textContent).not.toContain("Check the stream key");
    fireEvent.click(screen.getByTestId("card-url-fix").querySelector("button")!);
    expect(onEdit).toHaveBeenCalledTimes(1);
  });

  it("leaves an ordinary refusal as it was", () => {
    draw({ reason: reason() });
    const box = screen.getByTestId("card-failure-reason");
    expect(box.textContent).toContain("Stream key rejected");
    expect(screen.queryByTestId("card-url-fix")).toBeNull();
  });
});
