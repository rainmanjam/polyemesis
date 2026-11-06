// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { DestinationCard } from "./DestinationCard";
import type { DestStatus } from "@/lib/types";

/* "Refresh stream key" is offered exactly where the server can do it: the
 * capability matrix says streamKey is "yes" (the platform hands the key over).
 * It used to be hidden for custom and kick only, so Kick, which can, lost it
 * while Vimeo and X, which return ErrNoStreamKeyAPI on every path (a 400),
 * showed a button that always failed. */
afterEach(cleanup);

const dest = (platform: string): DestStatus =>
  ({
    id: 7,
    name: "Channel",
    kind: "rtmp",
    platform,
    enabled: true,
    summary: "",
    tracks: null,
    filterComplex: "",
    normalization: "off",
    warnings: null,
  }) as DestStatus;

function offered(platform: string): boolean {
  const noop = () => {};
  render(
    <MemoryRouter>
      <DestinationCard
        dest={dest(platform)}
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
  fireEvent.keyDown(screen.getByRole("button", { name: /Actions for Channel/ }), { key: "ArrowDown" });
  const found = screen.queryByText("Refresh stream key") !== null;
  cleanup();
  return found;
}

describe("the Refresh stream key action", () => {
  it.each(["youtube", "twitch", "facebook", "kick"])("is offered on %s, which hands the key over", (platform) => {
    expect(offered(platform)).toBe(true);
  });

  it.each(["vimeo", "x", "rumble", "custom"])("is not offered on %s, which has no key API", (platform) => {
    expect(offered(platform)).toBe(false);
  });
});
