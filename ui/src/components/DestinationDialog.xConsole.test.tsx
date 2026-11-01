// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* #3 in the editor: an X destination shows the X console, loads its stored go-live
 * title, and saves the edited title. Other platforms show no console. */

const updates = vi.hoisted(() => [] as Array<Record<string, unknown>>);

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
      platformGuides: () => Promise.resolve([{ platform: "x", livestreamGate: "closed" }]),
      services: () => Promise.resolve({}),
      updateDestination: (_id: number, body: Record<string, unknown>) => {
        updates.push(body);
        return Promise.resolve({ destination: { id: 9, ...body }, warnings: [] });
      },
    },
  };
});

import { DestinationDialog } from "./DestinationDialog";
import en from "@/lib/i18n/en.json";

const stored = (over: Record<string, unknown>) => ({
  id: 9,
  name: "X main",
  kind: "rtmp",
  platform: "x",
  url: "rtmps://a.live.x.com:443/x",
  streamKey: "k",
  sourceId: 1,
  enabled: false,
  goLiveTitle: "Sunday",
  ...over,
});

function open(destination: unknown) {
  return render(
    <DestinationDialog open onOpenChange={() => {}} destination={destination as never} onSaved={() => {}} />,
    { wrapper: MemoryRouter },
  );
}

describe("the X console in the destination editor", () => {
  afterEach(() => {
    cleanup();
    updates.length = 0;
  });

  it("loads the stored go-live title, shows the gate, and saves the edited title", async () => {
    open(stored({}));
    const input = (await waitFor(() => document.getElementById("x-go-live-title"))) as HTMLInputElement;
    expect(input.value).toBe("Sunday");
    await waitFor(() => expect(screen.getByText(en["wave3.x.gateClosed"])).toBeTruthy());
    fireEvent.change(input, { target: { value: "Sunday practice" } });
    fireEvent.click(screen.getByRole("button", { name: en["common.save"] }));
    await waitFor(() => expect(updates.length).toBe(1));
    expect(updates[0].goLiveTitle).toBe("Sunday practice");
  });

  it("shows no X console for another platform", async () => {
    open(stored({ platform: "twitch", url: "rtmp://live.twitch.tv/app", goLiveTitle: undefined }));
    await waitFor(() => expect(screen.getByRole("button", { name: en["common.save"] })).toBeTruthy());
    expect(document.getElementById("x-go-live-title")).toBeNull();
  });
});
