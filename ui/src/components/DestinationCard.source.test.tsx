// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { DestinationCard } from "./DestinationCard";
import type { DestStatus } from "@/lib/types";

/* WHICH PROGRAMME THIS CARD CARRIES.
 *
 * The dashboard draws every destination on the install in one grid. Before this
 * badge, two destinations called "Twitch" on different programmes were the same
 * card twice with nothing to tell them apart — and choosing the wrong one is
 * the mistake the whole source-picker change exists to prevent.
 *
 * jsdom rather than renderToStaticMarkup: this card is full of effects and
 * portal-rendered menus, and the static renderer runs none of them.
 */

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { ...actual.api } };
});

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

function draw(over: Partial<DestStatus>, sourceName?: string) {
  // The card links out to a platform artefact, so it needs a router in scope.
  return render(
    <MemoryRouter>
      <DestinationCard
        dest={dest(over)}
        sourceName={sourceName}
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

describe("the destination card's programme badge", () => {
  afterEach(cleanup);

  it("names the programme when the caller supplies one", () => {
    draw({ sourceId: 2 }, "Studio B");
    expect(screen.getByText("Studio B")).toBeTruthy();
  });

  it("says nothing when there is only one programme to be on", () => {
    // The Dashboard passes undefined on a single-source install. A badge on
    // every card reading the same word is noise that tells nobody anything,
    // and DestinationDialog already rules out a control whose every use has
    // the same outcome.
    draw({ sourceId: 1 }, undefined);
    expect(screen.queryByText("Main")).toBeNull();
    // The card still renders — "absent badge" must not mean "absent card".
    expect(screen.getByText("Twitch")).toBeTruthy();
  });

  it("does not colour the programme like a destination state", () => {
    // The five saturated tokens mean the STATE of a destination. A programme
    // name is not a state, and borrowing a signal colour for it would make an
    // ordinary label read as a condition — the mistake DestinationCard already
    // records for a hand-written text-ok that rendered a healthy backup
    // invisible.
    const { container } = draw({ sourceId: 2 }, "Studio B");
    const badge = screen.getByText("Studio B");
    const cls = badge.className;
    for (const signal of ["bg-ok", "bg-warn", "bg-danger", "bg-armed", "bg-live"]) {
      expect(cls.includes(signal)).toBe(false);
    }
    expect(container.textContent).toContain("Studio B");
  });

  it("does not let a long programme name push the state badge off the card", () => {
    // truncate + a max width, because the state badge beside it is the one
    // thing on this card that must never be pushed out of view.
    draw({ sourceId: 2 }, "a programme with a very long operator-chosen name");
    const badge = screen.getByText("a programme with a very long operator-chosen name");
    expect(badge.className).toContain("truncate");
  });
});
