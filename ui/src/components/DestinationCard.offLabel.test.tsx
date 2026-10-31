// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { DestinationCard } from "./DestinationCard";
import { translate } from "@/lib/i18n";
import type { DestStatus } from "@/lib/types";

/* app-council A2 and A89 on the destination card.
 *
 * A2: a destination the operator has switched off was labelled STOPPED, the
 * same word a broadcast that ended uses, so eight switched-off cards beside
 * five live ones read as eight faults. It is labelled Off.
 *
 * A89: the LIVE badge said so by fill colour and small text. A running card's
 * badge now carries a glyph as well, so the state reads without colour. */

afterEach(cleanup);

const dest = (over: Partial<DestStatus> = {}): DestStatus =>
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
    ...over,
  }) as DestStatus;

const noop = () => {};

function draw(over: Partial<DestStatus>) {
  return render(
    <MemoryRouter>
      <DestinationCard
        dest={dest(over)}
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

describe("DestinationCard status badge", () => {
  it("labels a switched-off destination Off, not Stopped", () => {
    draw({ enabled: false });
    const badge = screen.getByTestId("card-state-badge");
    expect(badge.textContent).toBe(translate("en", "state.off"));
    expect(badge.textContent).not.toBe(translate("en", "state.stopped"));
  });

  it("gives a running destination's badge a glyph, not colour alone", () => {
    draw({
      enabled: true,
      process: { name: "d1", kind: "destination", state: "running", pid: 1, restarts: 0, uptimeSec: 30 },
    } as Partial<DestStatus>);
    const badge = screen.getByTestId("card-state-badge");
    expect(badge.textContent).toBe(translate("en", "state.live"));
    expect(badge.querySelector("[data-badge-cue='on-air']")).not.toBeNull();
  });
});
