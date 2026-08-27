// @vitest-environment jsdom

/* THE FIRST TIER OF THE DASHBOARD, previously untested.
 *
 * Its whole claim is that the two questions an operator asks under load -- "am
 * I on air?" and "what is broken?" -- are answered above the inventory rather
 * than inside it. The failure modes follow from that: a headline that reads the
 * wrong state, counts that render as a steady zero (which teaches the eye to
 * skip the place a real one appears), and a fault list that is not empty when
 * nothing is wrong. */

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { OnAirBar } from "./OnAirBar";
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

const proc = (state: string, over: Partial<DestStatus> = {}) =>
  dest({ process: { state }, ...over } as Partial<DestStatus>);

const status = (destinations: DestStatus[] = []): Status =>
  ({ ingest: null, destinations }) as Status;

describe("OnAirBar headline", () => {
  it("reads ON AIR when something is running", () => {
    render(<OnAirBar status={status([proc("running")])} />);
    expect(screen.getByText("On air")).toBeTruthy();
  });

  it("reads COMING UP while a destination is still connecting", () => {
    // Distinct from both live and failed on purpose: an operator who reads
    // "Off air" during the connect window starts pulling things apart.
    render(<OnAirBar status={status([proc("starting")])} />);
    expect(screen.getByText("Coming up")).toBeTruthy();
  });

  it("reads OFF AIR when an enabled destination has failed", () => {
    render(<OnAirBar status={status([proc("failed")])} />);
    expect(screen.getByText("Off air")).toBeTruthy();
  });

  it("reads OFF AIR on an install with nothing enabled, which is not a fault", () => {
    render(<OnAirBar status={status([dest({ enabled: false })])} />);
    expect(screen.getByText("Off air")).toBeTruthy();
  });
});

describe("OnAirBar counts", () => {
  it("names the live-over-enabled ratio in words, not as a bare fraction", () => {
    // "2/3" alone gets read as a bitrate on a console full of them.
    render(<OnAirBar status={status([proc("running"), proc("running", { id: did(2) }), proc("failed", { id: did(3) })])} />);
    expect(screen.getByText(/destinations live/)).toBeTruthy();
  });

  it("renders no count at all when it would be zero", () => {
    // A steady "0 failed" is a number that teaches the eye to skip the place
    // where a real one will appear.
    render(<OnAirBar status={status([proc("running")])} />);
    expect(screen.queryByText(/failed/)).toBeNull();
    expect(screen.queryByText(/connecting/)).toBeNull();
  });

  it("renders the failed count once there is something to count", () => {
    render(<OnAirBar status={status([proc("failed")])} />);
    expect(screen.getByText(/1 failed/)).toBeTruthy();
  });
});

describe("OnAirBar fault list", () => {
  it("is EMPTY when nothing is wrong, and that emptiness is the feature", () => {
    // A panel that always has something in it is decoration, and an operator
    // stops reading it within a week -- which costs them the shift it mattered.
    const { container } = render(<OnAirBar status={status([proc("running")])} />);
    expect(container.querySelector("ul")).toBeNull();
  });

  it("lists a failed destination by name", () => {
    render(<OnAirBar status={status([proc("failed", { name: "YouTube" })])} />);
    const list = screen.getByRole("list");
    expect(list.textContent).toContain("YouTube");
  });
});

describe("OnAirBar across-the-room", () => {
  it("keeps the large-type view closed until it is asked for", () => {
    render(<OnAirBar status={status([proc("running")])} />);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByRole("button", { name: /across the room/i })).toBeTruthy();
  });
});
