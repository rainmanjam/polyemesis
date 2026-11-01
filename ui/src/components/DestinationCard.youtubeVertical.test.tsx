// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { DestinationCard } from "./DestinationCard";
import type { DestStatus } from "@/lib/types";

// A YouTube pair is one card (#7): the vertical half is a sub-row inside it, and until the
// operator confirms the one-time Live Control Room step the card keeps saying so.

vi.mock("@/lib/api", () => ({
  api: { youtubeVerticalStatus: vi.fn(async () => { throw new Error("not read"); }) },
  ApiError: class extends Error {},
}));

afterEach(cleanup);

const base = (over: Partial<DestStatus>): DestStatus =>
  ({ id: 1, name: "YouTube main", kind: "rtmp", platform: "youtube", enabled: true,
     summary: "", tracks: null, filterComplex: "", normalization: "off", warnings: null,
     ...over }) as DestStatus;

function draw(dest: DestStatus, vertical?: DestStatus) {
  const noop = () => {};
  return render(
    <MemoryRouter>
      <DestinationCard
        dest={dest}
        vertical={vertical}
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

describe("a YouTube pair on one card", () => {
  it("draws the vertical half as a sub-row of its horizontal card", () => {
    draw(
      base({ youtubeVertical: { verticalId: 2 as DestStatus["id"], pairingConfirmed: true } }),
      base({ id: 2 as DestStatus["id"], name: "YouTube main (vertical)", verticalHalfOf: 1 as DestStatus["id"] }),
    );
    expect(screen.getByTestId("youtube-vertical-subrow")).toBeTruthy();
  });

  it("keeps the reminder on the card until the Live Control Room step is confirmed", () => {
    draw(base({ youtubeVertical: { verticalId: 2 as DestStatus["id"], pairingConfirmed: false } }));
    expect(screen.getByTestId("youtube-vertical-reminder")).toBeTruthy();
  });

  it("drops the reminder once the pairing is confirmed", () => {
    draw(base({ youtubeVertical: { verticalId: 2 as DestStatus["id"], pairingConfirmed: true } }));
    expect(screen.queryByTestId("youtube-vertical-reminder")).toBeNull();
  });

  it("draws no sub-row for a destination with no vertical half", () => {
    draw(base({}));
    expect(screen.queryByTestId("youtube-vertical-subrow")).toBeNull();
  });
});
