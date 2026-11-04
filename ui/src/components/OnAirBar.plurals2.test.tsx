// @vitest-environment jsdom

/* The on-air strip reads one as one. The catalogue has no plural rules, so
 * the connecting count has its own key at 1 (German: "1 verbindet", not
 * "1 verbinden"), and the live/stopped/off line was rewritten in Spanish to a
 * count-neutral "label: value" form ("detenidos: 1", not "1 detenidos"). */

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { OnAirBar } from "./OnAirBar";
import type { DestStatus, Status } from "@/lib/types";
import { setLanguage } from "@/lib/i18n";

afterEach(cleanup);
afterEach(() => setLanguage("en"));

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

const proc = (id: number, state: string) =>
  dest({ id: did(id), name: `Dest ${id}`, process: { state } } as Partial<DestStatus>);

const switchedOff = (id: number) =>
  dest({ id: did(id), name: `Dest ${id}`, enabled: false, process: null } as Partial<DestStatus>);

const status = (destinations: DestStatus[] = []): Status =>
  ({ ingest: null, destinations }) as Status;

describe("OnAirBar: the connecting count reads one as one (de)", () => {
  it("says 1 verbindet with one destination connecting", () => {
    setLanguage("de");
    render(<OnAirBar status={status([proc(1, "starting")])} />);

    expect(screen.getByText("1 verbindet")).toBeTruthy();
    expect(screen.queryByText("1 verbinden")).toBeNull();
  });

  it("says 2 verbinden with two destinations connecting", () => {
    setLanguage("de");
    render(<OnAirBar status={status([proc(1, "starting"), proc(2, "starting")])} />);

    expect(screen.getByText("2 verbinden")).toBeTruthy();
  });
});

describe("OnAirBar: the live/stopped/off line is count-neutral (es)", () => {
  it("reads en directo: 1 · detenidos: 1 · apagados: 1", () => {
    setLanguage("es");
    // One running, one enabled but stopped, one switched off.
    render(<OnAirBar status={status([proc(1, "running"), proc(2, "stopped"), switchedOff(3)])} />);

    const counts = screen.getByTestId("on-air-counts");
    expect(counts.textContent).toBe("en directo: 1 · detenidos: 1 · apagados: 1");
    expect(screen.queryByText("1 en directo · 1 detenidos · 1 apagados")).toBeNull();
  });

  it("reads en directo: 2 · detenidos: 2 · apagados: 2", () => {
    setLanguage("es");
    render(
      <OnAirBar
        status={status([
          proc(1, "running"),
          proc(2, "running"),
          proc(3, "stopped"),
          proc(4, "stopped"),
          switchedOff(5),
          switchedOff(6),
        ])}
      />,
    );

    expect(screen.getByTestId("on-air-counts").textContent).toBe("en directo: 2 · detenidos: 2 · apagados: 2");
  });
});
