// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { DestinationCard } from "./DestinationCard";
import { OnAirBar } from "./OnAirBar";
import { destinationStatus, processStatus } from "@/lib/status";
import { translate } from "@/lib/i18n";
import type { DestStatus, Status } from "@/lib/types";

/* app-council A91: ONE WORD PER MEANING, ON EVERY SURFACE.
 *
 * The live Dashboard said "2 stopped" in the on-air counts, OFF on the cards
 * those two were, "Offline" on the ingest chip and "idle" in the Pipeline card:
 * four spellings of "not running" side by side. The console now has two words
 * for it, and every surface takes them from lib/status.ts:
 *
 *   - Off: the operator switched it off.
 *   - Stopped: it is on, and nothing is running.
 *
 * These tests put one switched-off destination and one enabled-but-idle
 * destination through each surface and require the same word from each. */

afterEach(cleanup);

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

const noop = () => {};

function card(d: DestStatus) {
  return render(
    <MemoryRouter>
      <DestinationCard
        dest={d}
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

describe("one status vocabulary across the console (app-council A91)", () => {
  it("a switched-off destination reads Off on its card and is counted as off in the on-air bar", () => {
    const off = dest({ enabled: false, process: null } as Partial<DestStatus>);
    card(off);
    expect(screen.getByTestId("card-state-badge").textContent).toBe(translate("en", "state.off"));
    cleanup();
    render(<OnAirBar status={{ ingest: null, destinations: [off] } as unknown as Status} />);
    // Old code counted it as "1 stopped" beside a card that said Off.
    expect(within(screen.getByTestId("on-air-counts")).getByText(/1 off/)).toBeTruthy();
    expect(screen.getByTestId("on-air-counts").textContent).toMatch(/0 stopped/);
  });

  it("an enabled destination with no process reads Stopped on its card and in the counts, never Offline", () => {
    const idle = dest({ enabled: true, process: null } as Partial<DestStatus>);
    card(idle);
    expect(screen.getByTestId("card-state-badge").textContent).toBe("Stopped");
    cleanup();
    render(<OnAirBar status={{ ingest: null, destinations: [idle] } as unknown as Status} />);
    expect(screen.getByTestId("on-air-counts").textContent).toMatch(/1 stopped · 0 off/);
  });

  it("the status map gives Off for switched off and Stopped for an absent process, in one place", () => {
    expect(destinationStatus(false, "running").labelKey).toBe("state.off");
    expect(destinationStatus(true, undefined).labelKey).toBe("state.stopped");
    expect(processStatus(null).labelKey).toBe("state.stopped");
  });
});
