// @vitest-environment jsdom

/* A stopped destination playing its broadcast-delay tail out is still on the
 * air: the engine's alert projection and MQTT say so, and so must every view,
 * with the counts agreeing (never "1 of 0") and the card badge lit. */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { AcrossTheRoom } from "./AcrossTheRoom";
import { DestinationCard } from "./DestinationCard";
import { OnAirBar } from "./OnAirBar";
import { onAir } from "@/lib/attention";
import type { DestStatus, Status } from "@/lib/types";

afterEach(cleanup);

const draining = {
  id: 1,
  name: "Twitch",
  kind: "rtmp",
  platform: "twitch",
  enabled: false,
  summary: "",
  tracks: null,
  filterComplex: "",
  normalization: "off",
  warnings: null,
  delay: { phase: "draining", seconds: 30 },
  process: { name: "dest:1", kind: "destination", state: "running", pid: 1, restarts: 0, uptimeSec: 90, progress: { bitrateKbps: 4500 } },
} as unknown as DestStatus;

const status = { ingest: null, destinations: [draining] } as unknown as Status;

describe("a destination draining its delay tail", () => {
  it("is live and enabled in the same total", () => {
    expect(onAir(status.destinations)).toMatchObject({ live: 1, enabled: 1, degraded: 0, failed: 0 });
  });

  it("reads '1 of 1' and keeps its row across the room", () => {
    render(<AcrossTheRoom open onClose={() => {}} status={status} />);
    expect(screen.getByText("On air")).toBeTruthy();
    expect(document.body.textContent).toContain("1 of 1");
    expect(document.body.textContent).not.toContain("1 of 0");
    expect(screen.getByText("Twitch")).toBeTruthy();
  });

  it("reads On air in the bar, with nothing counted as switched off", () => {
    render(<OnAirBar status={status} />);
    expect(screen.getByText("On air")).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/\b1 off\b/i);
  });

  it("draws the card badge on air with the on-air cue, not Off", () => {
    const noop = () => {};
    render(
      <MemoryRouter>
        <DestinationCard
          dest={draining}
          onStart={noop}
          onStop={noop}
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
    const badge = screen.getByTestId("card-state-badge");
    expect(badge.textContent).toBe("Live");
    expect(badge.querySelector('[data-badge-cue="on-air"]')).not.toBeNull();
  });
});
