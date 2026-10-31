// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* #11 in the editor: the broadcast delay is a number of seconds, 0 to 900, and
 * changing it on a destination that is on says BEFORE the save that the
 * destination restarts and refills -- a running buffer cannot be stretched. */

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      listSources: () => Promise.resolve([{ id: 1, name: "Main", publishUrls: {}, isDefault: true }]),
      listAccounts: () => Promise.resolve([]),
      listRenditions: () => Promise.resolve([]),
      listPlatformPresets: () => Promise.resolve([]),
      services: () => Promise.resolve({}),
    },
  };
});

import { DestinationDialog } from "./DestinationDialog";

const stored = (over: Record<string, unknown>) => ({
  id: 9,
  name: "YouTube",
  kind: "rtmp",
  platform: "custom",
  url: "rtmp://example.invalid/live",
  streamKey: "k",
  sourceId: 1,
  enabled: true,
  delaySeconds: 30,
  ...over,
});

function open(destination: unknown) {
  return render(
    <DestinationDialog open onOpenChange={() => {}} destination={destination as never} onSaved={() => {}} />,
    { wrapper: MemoryRouter },
  );
}

describe("the broadcast delay in the destination editor", () => {
  afterEach(cleanup);

  it("loads the stored delay and warns before a live destination's delay changes", async () => {
    open(stored({}));
    const input = (await waitFor(() => document.getElementById("dest-delay"))) as HTMLInputElement;
    expect(input.value).toBe("30");
    expect(screen.queryByTestId("delay-restart-warning")).toBeNull();
    fireEvent.change(input, { target: { value: "120" } });
    expect(screen.getByTestId("delay-restart-warning").textContent).toContain("120 s");
  });

  it("clamps the delay to the agreed fifteen minutes", async () => {
    open(stored({ enabled: false }));
    const input = (await waitFor(() => document.getElementById("dest-delay"))) as HTMLInputElement;
    fireEvent.change(input, { target: { value: "5000" } });
    expect(input.value).toBe("900");
    // A stopped destination restarts nothing, so there is nothing to warn about.
    expect(screen.queryByTestId("delay-restart-warning")).toBeNull();
  });
});
