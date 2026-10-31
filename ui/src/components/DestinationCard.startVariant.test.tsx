// @vitest-environment jsdom

/* THE PER-CARD START IS A SECONDARY ACTION (A9).
 *
 * Go live is the one filled start action on the Dashboard. A stopped card used
 * to carry its own filled green Start, which looked like the main action and
 * skipped the readiness check the sheet does. The card's Start is now an outline
 * button; green is kept for a live state only. */

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { DestinationCard } from "./DestinationCard";
import type { DestStatus } from "@/lib/types";

afterEach(cleanup);

const stopped = () =>
  ({
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
  }) as unknown as DestStatus;

describe("DestinationCard: the per-card Start is not the filled primary (A9)", () => {
  it("draws a stopped card's Start as an outline, not the filled green live variant", () => {
    const noop = () => {};
    render(
      <MemoryRouter>
        <DestinationCard
          dest={stopped()}
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
    const start = screen.getByRole("button", { name: /start/i });
    // The filled live variant is the one with the live background. The outline
    // variant carries a border and no fill.
    expect(start.className).not.toContain("bg-live");
    expect(start.className).toContain("border-border-strong");
  });
});
