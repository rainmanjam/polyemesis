// @vitest-environment jsdom

/* THE SECONDARY COUNTERS ARE QUIET WHEN THEY ARE QUIET.
 *
 * Six equal stat columns gave a clean run and a dropping one the same weight,
 * so zeros were noise and a non-zero Dropped did not stand out. Bitrate and
 * uptime now lead; restarts, dropped, duplicated and speed sit in a secondary
 * row that stays muted at zero and turns warn only when there is something to
 * look at. The dash-not-zero rule (#787) is unchanged and tested elsewhere. */

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { DestinationCard } from "./DestinationCard";
import type { DestStatus } from "@/lib/types";

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { ...actual.api } };
});

afterEach(cleanup);

const running = (progress: Record<string, number>, process: Record<string, unknown> = {}) =>
  ({
    id: 1,
    name: "Twitch",
    kind: "rtmp",
    platform: "twitch",
    enabled: true,
    summary: "",
    tracks: null,
    filterComplex: "",
    normalization: "off",
    warnings: null,
    process: { state: "running", uptimeSec: 120, restarts: 0, progress, ...process },
  }) as unknown as DestStatus;

function draw(dest: DestStatus) {
  const noop = () => {};
  return render(
    <MemoryRouter>
      <DestinationCard
        dest={dest}
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
}

describe("DestinationCard secondary counters", () => {
  it("keeps bitrate and uptime in the primary row, apart from the counters", () => {
    const { container } = draw(running({ bitrateKbps: 2500, speed: 1 }));
    const secondary = container.querySelector('[data-testid="card-secondary-stats"]');
    expect(secondary).toBeTruthy();
    // The row directly above it is the primary pair, and it carries the bitrate.
    const primary = (secondary as HTMLElement).previousElementSibling as HTMLElement;
    expect(primary.textContent).toMatch(/Mbps|kbps/);
    expect(within(secondary as HTMLElement).queryByText(/Mbps|kbps/)).toBeNull();
  });

  it("draws the counters muted when every one of them is zero", () => {
    const { container } = draw(running({ bitrateKbps: 2500, speed: 1, dropFrames: 0, dupFrames: 0 }));
    const secondary = container.querySelector('[data-testid="card-secondary-stats"]') as HTMLElement;
    expect(secondary.querySelector(".text-warn")).toBeNull();
  });

  it("turns a non-zero dropped count warn, where it cannot be missed", () => {
    const { container } = draw(running({ bitrateKbps: 2500, speed: 1, dropFrames: 14, dupFrames: 0 }));
    const secondary = container.querySelector('[data-testid="card-secondary-stats"]') as HTMLElement;
    expect(secondary.querySelector(".text-warn")).not.toBeNull();
  });

  it("names a running destination once, in the badge, not as a dot and a badge", () => {
    const { container } = draw(running({ bitrateKbps: 2500, speed: 1 }));
    expect(container.querySelector('[data-testid="card-status-dot"]')).toBeNull();
  });

  it("keeps the dot on a stopped card, where the outline badge needs a mark of its tone", () => {
    const { container } = draw({ ...running({}), enabled: false } as DestStatus);
    expect(container.querySelector('[data-testid="card-status-dot"]')).not.toBeNull();
  });
});
