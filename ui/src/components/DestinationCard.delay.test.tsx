// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { DestinationCard } from "./DestinationCard";
import { asDestinationId, type DelayDumpResult, type DelayStatus, type DestStatus } from "@/lib/types";

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
  effectiveSeconds: 30,
  dumpable: true,
  memBytes: 0,
  diskBytes: 0,
  ...over,
});

function draw(
  over: Partial<DestStatus>,
  onStopNow?: () => void,
  onDump?: (seconds?: number) => Promise<DelayDumpResult>,
) {
  return render(card(over, onStopNow, onDump));
}

function card(
  over: Partial<DestStatus>,
  onStopNow?: () => void,
  onDump?: (seconds?: number) => Promise<DelayDumpResult>,
) {
  const noop = () => {};
  return (
    <MemoryRouter>
      <DestinationCard
        dest={dest(over)}
        onStart={noop}
        onStop={noop}
        onStopNow={onStopNow}
        onDump={onDump}
        onRestart={noop}
        onEdit={noop}
        onDelete={noop}
        onRefreshKey={noop}
        onMoveEarlier={noop}
        onMoveLater={noop}
        canMoveEarlier={false}
        canMoveLater={false}
      />
    </MemoryRouter>
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

const answer = (over: Partial<DelayDumpResult> = {}): DelayDumpResult => ({
  id: asDestinationId(1),
  name: "YouTube main",
  complete: true,
  requestedSeconds: 0,
  dumpedSeconds: 11.4,
  clamped: false,
  delayBeforeSeconds: 30,
  delayAfterSeconds: 18.6,
  configuredSeconds: 30,
  restoresOnNextStart: true,
  ...over,
});

/* Dump: drop the newest part of the buffer while staying on the air. The delay
 * stays shorter until the destination next starts, so the card says how long
 * the audience's delay is NOW, and the dump is confirmed before it is sent. */
describe("dumping a delayed destination", () => {
  const dialog = () => within(screen.getByRole("dialog"));
  const openDump = () => fireEvent.click(screen.getByTestId("delay-dump"));
  const confirm = () => fireEvent.click(dialog().getByRole("button", { name: "Dump" }));

  it("shows the effective delay once some of it has been dumped", () => {
    draw({ delay: delay({ effectiveSeconds: 18, dumpedSeconds: 12 }) });
    expect(screen.getByTestId("delay-badge").textContent).toContain("Delayed 18 of 30 s");
    expect(screen.getByTestId("delay-restores").textContent).toContain("full 30 s delay returns");
  });

  it("says nothing about restoring when nothing has been dumped", () => {
    draw({ delay: delay({}) }, undefined, vi.fn());
    expect(screen.queryByTestId("delay-restores")).toBeNull();
  });

  it("offers Dump only while the delayed destination is on the air", () => {
    const dump = vi.fn();
    draw({ delay: delay({ phase: "buffering", fill: 0.4, bufferedSeconds: 12 }) }, undefined, dump);
    expect(screen.queryByTestId("delay-dump")).toBeNull();
    cleanup();
    draw({ enabled: false, delay: delay({ phase: "draining", drainRemainingSeconds: 5 }) }, undefined, dump);
    expect(screen.queryByTestId("delay-dump")).toBeNull();
    cleanup();
    draw({ delay: delay({}) }, undefined, dump);
    expect(screen.getByTestId("delay-dump").hasAttribute("disabled")).toBe(false);
  });

  it("disables Dump when there is nothing to cut at, or a dump is in flight", () => {
    draw({ delay: delay({ dumpable: false }) }, undefined, vi.fn());
    expect(screen.getByTestId("delay-dump").hasAttribute("disabled")).toBe(true);
    cleanup();
    draw({ delay: delay({ dumping: true }) }, undefined, vi.fn());
    expect(screen.getByTestId("delay-dump").hasAttribute("disabled")).toBe(true);
  });

  it("asks first: nothing is sent until the dump is confirmed", () => {
    const dump = vi.fn(async () => answer());
    draw({ delay: delay({}) }, undefined, dump);
    openDump();
    expect(dump).not.toHaveBeenCalled();
    expect(dialog().getByTestId("delay-dump-before").textContent).toContain("30 s");
    fireEvent.click(dialog().getByRole("button", { name: "Cancel" }));
    expect(dump).not.toHaveBeenCalled();
  });

  // An explicit 0, not "no seconds": with no seconds the server applies the
  // destination's own dump default, which is not what this option says.
  it("dumps everything since the last dump point by default, as an explicit 0", async () => {
    const dump = vi.fn(async () => answer());
    draw({ delay: delay({}) }, undefined, dump);
    openDump();
    expect((dialog().getByTestId("delay-dump-mode-since") as HTMLInputElement).checked).toBe(true);
    expect(dialog().getByTestId("delay-dump-after").textContent).toContain("2 s");
    confirm();
    await waitFor(() => expect(dump).toHaveBeenCalledTimes(1));
    expect(dump).toHaveBeenCalledWith(0);
  });

  it("preselects the destination's own dump default", async () => {
    const dump = vi.fn(async () => answer({ requestedSeconds: 8 }));
    draw({ delayDumpSeconds: 8, delay: delay({}) }, undefined, dump);
    openDump();
    expect((dialog().getByTestId("delay-dump-mode-seconds") as HTMLInputElement).checked).toBe(true);
    expect((dialog().getByTestId("delay-dump-seconds") as HTMLInputElement).value).toBe("8");
    expect(dialog().getByTestId("delay-dump-after").textContent).toContain("22 s");
    confirm();
    await waitFor(() => expect(dump).toHaveBeenCalledTimes(1));
    expect(dump).toHaveBeenCalledWith(8);
  });

  it("still sends 0 when the operator overrides a default with everything since the last dump point", async () => {
    const dump = vi.fn(async () => answer());
    draw({ delayDumpSeconds: 8, delay: delay({}) }, undefined, dump);
    openDump();
    fireEvent.click(dialog().getByTestId("delay-dump-mode-since"));
    confirm();
    await waitFor(() => expect(dump).toHaveBeenCalledTimes(1));
    expect(dump).toHaveBeenCalledWith(0);
  });

  it("warns that a dump can take up to about two keyframe intervals more than asked", () => {
    draw({ delay: delay({}) }, undefined, vi.fn());
    openDump();
    expect(dialog().getByTestId("delay-dump-overshoot").textContent).toContain("two keyframe intervals");
    fireEvent.click(dialog().getByTestId("delay-dump-mode-seconds"));
    expect(dialog().getByTestId("delay-dump-overshoot")).toBeTruthy();
  });

  it("dumps a number of seconds when one is chosen, and estimates what is left", async () => {
    const dump = vi.fn(async () => answer({ requestedSeconds: 12 }));
    draw({ delay: delay({}) }, undefined, dump);
    openDump();
    fireEvent.click(dialog().getByTestId("delay-dump-mode-seconds"));
    fireEvent.change(dialog().getByTestId("delay-dump-seconds"), { target: { value: "12" } });
    expect(dialog().getByTestId("delay-dump-after").textContent).toContain("18 s");
    confirm();
    await waitFor(() => expect(dump).toHaveBeenCalledTimes(1));
    expect(dump).toHaveBeenCalledWith(12);
  });

  it("never estimates less than 2 s left", () => {
    draw({ delay: delay({ effectiveSeconds: 8 }) }, undefined, vi.fn());
    openDump();
    fireEvent.click(dialog().getByTestId("delay-dump-mode-seconds"));
    expect(dialog().getByTestId("delay-dump-after").textContent).toContain("about 2 s");
  });

  it("reports the server's figures, not the estimate", async () => {
    draw({ delay: delay({}) }, undefined, async () => answer());
    openDump();
    confirm();
    const done = await screen.findByTestId("delay-dump-result");
    expect(done.textContent).toContain("Dumped 11.4 s");
    expect(done.textContent).toContain("delay now 18.6 s");
    expect(done.textContent).not.toContain("Less than asked");
  });

  it("says when less could be dropped than asked", async () => {
    draw({ delay: delay({}) }, undefined, async () =>
      answer({ clamped: true, dumpedSeconds: 4.2, delayAfterSeconds: 25.8 }),
    );
    openDump();
    confirm();
    const done = await screen.findByTestId("delay-dump-result");
    expect(done.textContent).toContain("Dumped 4.2 s");
    expect(done.textContent).toContain("Less than asked");
  });

  it("forgets a dump's figures once the destination has stopped and started again", async () => {
    const dump = async () => answer();
    const { rerender } = draw({ delay: delay({}) }, undefined, dump);
    openDump();
    confirm();
    await screen.findByTestId("delay-dump-result");
    rerender(card({ enabled: false, delay: delay({ phase: "draining", drainRemainingSeconds: 5 }) }, undefined, dump));
    rerender(card({ delay: delay({}) }, undefined, dump));
    expect(screen.getByTestId("delay-badge").textContent).toContain("Delayed 30 s");
    expect(screen.queryByTestId("delay-dump-result")).toBeNull();
  });

  it("shows the server's reason when there is nothing to dump", async () => {
    draw({ delay: delay({}) }, undefined, async () => {
      throw new Error("nothing to dump yet");
    });
    openDump();
    confirm();
    const failed = await screen.findByTestId("delay-dump-result");
    expect(failed.textContent).toContain("nothing to dump yet");
  });
});
