// @vitest-environment jsdom

/* DEGRADED-LINK FAILOVER'S THRESHOLDS (#6). The ways this form can fail are
 * the ways a threshold silently stops being the one the operator typed: a
 * field wired to the wrong leaf, a switch that drops the numbers already set,
 * or defaults shown that differ from the ones the server applies. */

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { DegradedLinkSettings } from "./DegradedLinkSettings";
import type { DegradedSettings } from "@/lib/types";

afterEach(cleanup);

describe("DegradedLinkSettings", () => {
  it("shows the thresholds while off too, so they can be read before switching on", () => {
    render(<DegradedLinkSettings value={{ enabled: false }} onChange={() => {}} />);
    expect((screen.getByRole("switch") as HTMLElement).getAttribute("aria-checked")).toBe("false");
    expect(screen.getAllByRole("spinbutton")).toHaveLength(4);
  });

  it("shows the server's defaults when switched on with nothing stored", () => {
    render(<DegradedLinkSettings value={{ enabled: true }} onChange={() => {}} />);
    const values = screen.getAllByRole("spinbutton").map((el) => (el as HTMLInputElement).value);
    // 800 kbps, 5 s, 1500 ms, 15 % -- internal/db DefaultSettings.
    expect(values).toEqual(["800", "5", "1500", "15"]);
  });

  it("writes each field to its own leaf and keeps the others", () => {
    const onChange = vi.fn<(next: DegradedSettings) => void>();
    const value: DegradedSettings = {
      enabled: true, minBitrateKbps: 900, maxRttMs: 1200, maxLossPercent: 10, sustainSeconds: 7,
    };
    render(<DegradedLinkSettings value={value} onChange={onChange} />);
    const [bitrate, sustain, rtt, loss] = screen.getAllByRole("spinbutton");
    fireEvent.change(bitrate, { target: { value: "1500" } });
    expect(onChange).toHaveBeenLastCalledWith({ ...value, minBitrateKbps: 1500 });
    fireEvent.change(sustain, { target: { value: "3" } });
    expect(onChange).toHaveBeenLastCalledWith({ ...value, sustainSeconds: 3 });
    fireEvent.change(rtt, { target: { value: "0" } });
    expect(onChange).toHaveBeenLastCalledWith({ ...value, maxRttMs: 0 });
    fireEvent.change(loss, { target: { value: "2.5" } });
    expect(onChange).toHaveBeenLastCalledWith({ ...value, maxLossPercent: 2.5 });
  });

  it("switching off keeps the thresholds, so switching back on restores them", () => {
    const onChange = vi.fn<(next: DegradedSettings) => void>();
    const value: DegradedSettings = { enabled: true, minBitrateKbps: 1234 };
    render(<DegradedLinkSettings value={value} onChange={onChange} />);
    fireEvent.click(screen.getByRole("switch"));
    expect(onChange).toHaveBeenLastCalledWith({ enabled: false, minBitrateKbps: 1234 });
  });
});
