// @vitest-environment jsdom

/* The across-the-room view reads one as one. The catalogue has no plural
 * rules, so "{live} of {enabled} destinations" has its own key when a single
 * destination is enabled: it read "1 of 1 destinations". */

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { AcrossTheRoom } from "./AcrossTheRoom";
import type { DestStatus, Status } from "@/lib/types";
import { setLanguage } from "@/lib/i18n";

afterEach(cleanup);
afterEach(() => setLanguage("en"));

const did = (n: number) => n as unknown as DestStatus["id"];

const dest = (id: number, state: string): DestStatus =>
  ({
    id: did(id),
    name: `Dest ${id}`,
    kind: "rtmp",
    platform: "twitch",
    enabled: true,
    summary: "",
    tracks: null,
    filterComplex: "",
    normalization: "off",
    warnings: null,
    process: { state, uptimeSec: state === "running" ? 60 : 0 },
  }) as unknown as DestStatus;

const status = (destinations: DestStatus[]): Status =>
  ({ ingest: null, destinations }) as unknown as Status;

describe("AcrossTheRoom: the live-of-enabled line reads one as one (en)", () => {
  it("says 1 of 1 destination with one destination enabled", () => {
    setLanguage("en");
    render(<AcrossTheRoom open onClose={() => {}} status={status([dest(1, "running")])} />);

    expect(screen.getByText("1 of 1 destination")).toBeTruthy();
    expect(screen.queryByText("1 of 1 destinations")).toBeNull();
  });

  it("says 1 of 2 destinations with two destinations enabled", () => {
    setLanguage("en");
    render(<AcrossTheRoom open onClose={() => {}} status={status([dest(1, "running"), dest(2, "stopped")])} />);

    expect(screen.getByText("1 of 2 destinations")).toBeTruthy();
  });
});
