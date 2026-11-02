// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* #11 in the editor: the broadcast delay is a number of seconds, 0 to 900, and
 * changing it on a destination that is on says BEFORE the save that the
 * destination restarts and refills -- a running buffer cannot be stretched. */

const updateDestination = vi.fn();

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
      updateDestination: (id: unknown, body: unknown) => updateDestination(id, body),
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

  // YouTube holds a caption POST destination's delay (broadcastStreamDelayMs), so
  // changing it moves nothing in polyemesis's output and restarts nothing here.
  // MUTATION: the warning comparing the configured delay again. Observed: FAIL,
  // the restart warning was shown.
  it("does not warn of a restart when a YouTube caption POST destination's delay changes", async () => {
    open(stored({ platform: "youtube", captionMode: "youtube_post" }));
    const input = (await waitFor(() => document.getElementById("dest-delay"))) as HTMLInputElement;
    expect(input.value).toBe("30");
    fireEvent.change(input, { target: { value: "60" } });
    expect(screen.queryByTestId("delay-restart-warning")).toBeNull();
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

/* The Dump's default (owner decision A): how much a Dump drops when the
 * operator does not say. 0 is "everything since the last dump point". Only
 * meaningful with a delay, so it sits beside the delay and goes with it. */
describe("the dump default in the destination editor", () => {
  beforeEach(() => updateDestination.mockReset().mockResolvedValue({ warnings: [] }));
  afterEach(cleanup);

  const save = async () => {
    fireEvent.click(screen.getByRole("button", { name: /^Save$/ }));
    await waitFor(() => expect(updateDestination).toHaveBeenCalled());
    return updateDestination.mock.calls[0][1] as Record<string, unknown>;
  };

  it("loads the stored default and saves a changed one", async () => {
    open(stored({ enabled: false, delayDumpSeconds: 8 }));
    const input = (await waitFor(() => document.getElementById("dest-delay-dump"))) as HTMLInputElement;
    expect(input.value).toBe("8");
    fireEvent.change(input, { target: { value: "12" } });
    expect((await save()).delayDumpSeconds).toBe(12);
  });

  // The PUT is decoded over the stored row, so a 0 that is not sent is a
  // default that is never cleared.
  it("sends 0 when the default is cleared, and clamps to fifteen minutes", async () => {
    open(stored({ enabled: false, delayDumpSeconds: 8 }));
    const input = (await waitFor(() => document.getElementById("dest-delay-dump"))) as HTMLInputElement;
    fireEvent.change(input, { target: { value: "5000" } });
    expect(input.value).toBe("900");
    fireEvent.change(input, { target: { value: "0" } });
    expect((await save()).delayDumpSeconds).toBe(0);
  });

  it("is not offered without a broadcast delay", async () => {
    open(stored({ enabled: false, delaySeconds: 0 }));
    await waitFor(() => document.getElementById("dest-delay"));
    expect(document.getElementById("dest-delay-dump")).toBeNull();
  });
});
