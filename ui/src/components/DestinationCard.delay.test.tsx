// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { DestinationCard } from "./DestinationCard";
import type { DelayStatus, DestStatus } from "@/lib/types";

/* #11 on the destination card. A delayed destination says so; one filling its
 * delay shows how far it has got; one that was STOPPED and is still airing its
 * buffered tail says so in red with the control that cuts it -- a delay exists
 * to keep things off the air, so the kill switch is on the card. */
afterEach(cleanup);

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
    delaySeconds: 30,
    ...over,
  }) as DestStatus;

const delay = (over: Partial<DelayStatus>): DelayStatus => ({
  seconds: 30,
  phase: "on-air",
  fill: 1,
  bufferedSeconds: 30,
  memBytes: 0,
  diskBytes: 0,
  ...over,
});

function draw(over: Partial<DestStatus>, onStopNow?: () => void) {
  const noop = () => {};
  return render(
    <MemoryRouter>
      <DestinationCard
        dest={dest(over)}
        onStart={noop}
        onStop={noop}
        onStopNow={onStopNow}
        onRestart={noop}
        onEdit={noop}
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

describe("the destination card shows its broadcast delay", () => {
  it("marks a delayed destination that is on the air", () => {
    draw({ delay: delay({}) });
    expect(screen.getByTestId("delay-badge").textContent).toContain("Delayed 30 s");
  });

  it("shows the fill while the delay is still buffering", () => {
    draw({ delay: delay({ phase: "buffering", fill: 0.4, bufferedSeconds: 12 }) });
    expect(screen.getByTestId("delay-buffering").textContent).toContain("12 of 30 s");
    expect(screen.getByRole("progressbar").getAttribute("aria-valuenow")).toBe("40");
  });

  it("says a stopped destination is still airing its tail, and Cut now cuts it", () => {
    const cut = vi.fn();
    draw({ enabled: false, delay: delay({ phase: "draining", drainRemainingSeconds: 12.2 }) }, cut);
    const row = screen.getByTestId("delay-draining");
    expect(row.textContent).toContain("Airing delayed buffer: 13 s left");
    fireEvent.click(screen.getByRole("button", { name: /Cut now/ }));
    expect(cut).toHaveBeenCalledTimes(1);
  });

  it("offers Stop now beside Stop on a delayed destination that is on", () => {
    const now = vi.fn();
    draw({ delay: delay({}) }, now);
    fireEvent.click(screen.getByTestId("delay-stop-now"));
    expect(now).toHaveBeenCalledTimes(1);
  });

  it("shows nothing about a delay on a destination that has none", () => {
    draw({ delaySeconds: 0, delay: null });
    expect(screen.queryByTestId("delay-badge")).toBeNull();
    expect(screen.queryByTestId("delay-stop-now")).toBeNull();
  });
});
