// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* YouTube's liveBroadcasts contentDetails controls are per-destination settings
 * in the compliance block: loaded from the stored row, saved with it, and
 * collapsed to null when every control is back at "leave unchanged" (the PUT is
 * decoded over the stored row, so an omitted key would keep the old block). */

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

const stored = (compliance: Record<string, unknown>) => ({
  id: 9, name: "YouTube", kind: "rtmp", platform: "youtube", url: "rtmp://example.invalid/live",
  streamKey: "k", sourceId: 1, enabled: false, compliance,
});

function open(destination: unknown) {
  return render(
    <DestinationDialog open onOpenChange={() => {}} destination={destination as never} onSaved={() => {}} />,
    { wrapper: MemoryRouter },
  );
}

const field = async (id: string) => (await waitFor(() => document.getElementById(id))) as HTMLSelectElement;

const save = async () => {
  fireEvent.click(screen.getByRole("button", { name: /^Save$/ }));
  await waitFor(() => expect(updateDestination).toHaveBeenCalled());
  return (updateDestination.mock.calls[0][1] as { compliance: { youtube?: unknown } }).compliance;
};

describe("YouTube broadcast controls in the destination editor", () => {
  beforeEach(() => updateDestination.mockReset().mockResolvedValue({ warnings: [] }));
  afterEach(cleanup);

  it("shows the stored controls and saves a changed one with the rest kept", async () => {
    open(stored({ youtube: { latencyPreference: "low", enableDvr: false } }));
    expect((await field("yt-latency")).value).toBe("low");
    expect((await field("yt-enableDvr")).value).toBe("off");
    expect((await field("yt-recordFromStart")).value).toBe("");
    fireEvent.change(await field("yt-recordFromStart"), { target: { value: "on" } });
    fireEvent.change(await field("yt-captions"), { target: { value: "closedCaptionsEmbedded" } });
    expect((await save()).youtube).toEqual({
      latencyPreference: "low", enableDvr: false, recordFromStart: true, closedCaptionsType: "closedCaptionsEmbedded",
    });
  });

  it("collapses to null once every control is back at leave unchanged", async () => {
    open(stored({ youtube: { enableAutoStop: true } }));
    fireEvent.change(await field("yt-enableAutoStop"), { target: { value: "" } });
    expect((await save()).youtube).toBeNull();
  });

  it("offers every documented latency and caption value, and never the deprecated toggle", async () => {
    open(stored({}));
    const latency = Array.from((await field("yt-latency")).options).map((o) => o.value);
    expect(latency).toEqual(["", "normal", "low", "ultraLow"]);
    const captions = Array.from((await field("yt-captions")).options).map((o) => o.value);
    expect(captions).toEqual(["", "closedCaptionsDisabled", "closedCaptionsEmbedded", "closedCaptionsHttpPost"]);
    expect(document.getElementById("yt-enableClosedCaptions")).toBeNull();
    expect(document.getElementById("yt-madeForKids")).toBeNull();
  });
});
