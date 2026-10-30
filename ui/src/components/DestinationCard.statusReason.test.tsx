// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { DestinationCard } from "./DestinationCard";
import type { DestStatus } from "@/lib/types";

/* B1 on the destination card: a problem row is a non-pass status, so it names
 * its reason, the failing step, and when the reading was taken. These tests
 * pin the three facts to the row that carries the error, so a row that prints
 * the message alone fails here. */
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

describe("destination problem rows name their reason, step and as-of", () => {
  it("an error row names the reason and the failing output step", () => {
    draw({ error: "stream key rejected by ingest" });
    expect(screen.getByText("stream key rejected by ingest")).toBeTruthy();
    expect(screen.getByText(/Output to Twitch/)).toBeTruthy();
    expect(screen.getByText(/not recorded/i)).toBeTruthy();
  });

  it("a stop warning names the stop request as the failing step", () => {
    draw({ stopWarning: "stop not confirmed" });
    expect(screen.getByText("stop not confirmed")).toBeTruthy();
    expect(screen.getByText(/Stop request/)).toBeTruthy();
  });
});
