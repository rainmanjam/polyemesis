// @vitest-environment jsdom

/* THE VIEW YOU READ FROM TWO METRES AWAY, and it had no test at all.
 *
 * It is the one screen in the console whose entire job is a single word --
 * "On air" or "Off air" -- so the ways it can fail are narrow and severe: say
 * on-air when nothing is going out, keep answering Escape after it closes, or
 * quietly drop a destination from the list somebody walked over to read. Each
 * one is pinned below with its opposite, because a view that renders "Off air"
 * unconditionally would otherwise pass half of them. */

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { AcrossTheRoom } from "./AcrossTheRoom";
import type { DestStatus, Status } from "@/lib/types";

afterEach(cleanup);

/* DestinationId is branded on purpose -- a bare number cannot be one, which is
   what stops a source id being passed where a destination id belongs. Tests
   need ids all the same, so they are minted here, in one visible place. */
const did = (n: number) => n as unknown as DestStatus["id"];

const dest = (over: Partial<DestStatus> = {}): DestStatus =>
  ({
    id: did(1),
    name: "Twitch",
    kind: "rtmp",
    platform: "twitch",
    enabled: true,
    summary: "",
    tracks: null,
    filterComplex: "",
    normalization: "off",
    warnings: null,
    ...over,
  }) as DestStatus;

const status = (over: Partial<Status> = {}): Status =>
  ({ ingest: null, destinations: [], ...over }) as Status;

describe("AcrossTheRoom", () => {
  it("renders nothing at all while closed", () => {
    // Not "hidden": this is fixed to the viewport and covers the console, so a
    // version that stayed mounted would be a black screen nobody asked for.
    const { container } = render(
      <AcrossTheRoom open={false} onClose={() => {}} status={status()} />,
    );
    expect(container.innerHTML).toBe("");
  });

  it("says ON AIR when a destination is actually running", () => {
    render(
      <AcrossTheRoom
        open
        onClose={() => {}}
        status={status({
          destinations: [dest({ process: { state: "running" } } as Partial<DestStatus>)],
        })}
      />,
    );
    expect(screen.getByText("On air")).toBeTruthy();
    expect(screen.queryByText("Off air")).toBeNull();
  });

  it("says OFF AIR when the destinations are enabled but nothing is running", () => {
    // The pair that matters. A false "On air" is the worst thing this screen
    // can do: it is read from a distance precisely so nobody has to check.
    render(
      <AcrossTheRoom
        open
        onClose={() => {}}
        status={status({
          destinations: [dest({ process: { state: "stopped" } } as Partial<DestStatus>)],
        })}
      />,
    );
    expect(screen.getByText("Off air")).toBeTruthy();
    expect(screen.queryByText("On air")).toBeNull();
  });

  it("lists every ENABLED destination, not only the broken ones", () => {
    // The fault list answers "what is wrong". This answers "is the one I care
    // about still up", which is the question somebody walks over to ask.
    render(
      <AcrossTheRoom
        open
        onClose={() => {}}
        status={status({
          destinations: [
            dest({ id: did(1), name: "Twitch", process: { state: "running" } } as Partial<DestStatus>),
            dest({ id: did(2), name: "YouTube", process: { state: "running" } } as Partial<DestStatus>),
            dest({ id: did(3), name: "Kick", enabled: false }),
          ],
        })}
      />,
    );
    expect(screen.getByText("Twitch")).toBeTruthy();
    expect(screen.getByText("YouTube")).toBeTruthy();
    // Disabled is not "off air", it is not part of this broadcast at all.
    expect(screen.queryByText("Kick")).toBeNull();
  });

  it("closes on Escape, and stops listening once closed", () => {
    // Bound only while open, so the console keeps Escape for its dialogs the
    // rest of the time -- a stray global handler would close things the
    // operator was actually using.
    const onClose = vi.fn();
    const { rerender } = render(
      <AcrossTheRoom open onClose={onClose} status={status()} />,
    );
    fireEvent.keyDown(window, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);

    rerender(<AcrossTheRoom open={false} onClose={onClose} status={status()} />);
    fireEvent.keyDown(window, { key: "Escape" });
    expect(onClose, "Escape still fired after the view closed").toHaveBeenCalledTimes(1);
  });

  it("offers no controls, because a screen read from across the room cannot be aimed at", () => {
    // Close is the only button. A Stop button here is a Stop button somebody
    // leans on.
    render(
      <AcrossTheRoom
        open
        onClose={() => {}}
        status={status({
          destinations: [dest({ process: { state: "running" } } as Partial<DestStatus>)],
        })}
      />,
    );
    const buttons = screen.getAllByRole("button");
    expect(buttons).toHaveLength(1);
    expect(buttons[0].getAttribute("aria-label")).toBe("Close");
  });
});
