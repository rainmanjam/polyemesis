// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { GoLiveSheet } from "./GoLiveSheet";
import type { DestStatus } from "@/lib/types";
import type { GoLiveRow } from "@/lib/goLive";

// The go-live sheet is the last place to remind the operator about the vertical pairing
// (#7): Dual stream cannot be added after a stream has started, so the reminder stays on the
// sheet for any pair being started that still lacks its confirmed Live Control Room step.

vi.mock("@/lib/api", () => ({ api: {}, ApiError: class extends Error {} }));

afterEach(cleanup);

const row = (id: number): GoLiveRow =>
  ({ id, name: `d${id}`, platform: "youtube", enabled: false, sourceName: null }) as unknown as GoLiveRow;

const dest = (id: number, over: Partial<DestStatus> = {}): DestStatus =>
  ({ id, name: `d${id}`, kind: "rtmp", platform: "youtube", enabled: false, ...over }) as DestStatus;

describe("the go-live sheet's vertical reminder", () => {
  it("names the unconfirmed pairs among the rows being started", () => {
    render(
      <GoLiveSheet
        open
        onOpenChange={() => {}}
        rows={[row(1)]}
        live={[
          dest(1, { youtubeVertical: { verticalId: 2 as DestStatus["id"], pairingConfirmed: false } }),
          dest(3, { youtubeVertical: { verticalId: 4 as DestStatus["id"], pairingConfirmed: false } }),
        ]}
        youtubeAtRisk={0}
        onStart={async () => {}}
        onRetry={async () => {}}
        onFinished={() => {}}
      />,
    );
    expect(screen.getByTestId("golive-vertical-reminder").textContent).toMatch(/1 YouTube vertical pair/);
  });

  it("is silent when every pair being started is confirmed", () => {
    render(
      <GoLiveSheet
        open
        onOpenChange={() => {}}
        rows={[row(1)]}
        live={[dest(1, { youtubeVertical: { verticalId: 2 as DestStatus["id"], pairingConfirmed: true } })]}
        youtubeAtRisk={0}
        onStart={async () => {}}
        onRetry={async () => {}}
        onFinished={() => {}}
      />,
    );
    expect(screen.queryByTestId("golive-vertical-reminder")).toBeNull();
  });
});
