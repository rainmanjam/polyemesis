// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* A destination's presetId travels only when it means something: the preset the
 * operator picked in this dialog, or the one already stored. The label guess the
 * picker shows for a destination that predates the catalogue (an exact URL, or
 * the first preset for the platform) is never sent. */

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
      platformGuides: () => Promise.resolve([]),
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
  name: "Twitch main",
  kind: "rtmp",
  platform: "twitch",
  url: "rtmp://live.twitch.tv/app",
  streamKey: "k",
  sourceId: 1,
  enabled: false,
  ...over,
});

beforeAll(() => {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.releasePointerCapture = () => {};
  window.HTMLElement.prototype.setPointerCapture = () => {};
  window.HTMLElement.prototype.scrollIntoView = () => {};
});

function open(destination: unknown) {
  return render(
    <DestinationDialog open onOpenChange={() => {}} destination={destination as never} onSaved={() => {}} />,
    { wrapper: MemoryRouter },
  );
}

async function save() {
  await waitFor(() => expect(screen.getByRole("button", { name: en["common.save"] })).toBeTruthy());
  fireEvent.click(screen.getByRole("button", { name: en["common.save"] }));
  await waitFor(() => expect(updates.length).toBe(1), { timeout: 4000 });
  return updates[0];
}

describe("the preset id a destination saves", () => {
  afterEach(() => {
    cleanup();
    updates.length = 0;
  });

  // The picker labels this destination "Mux Video" by matching its URL exactly.
  // That guess is not something the operator chose and must not be stored.
  it("does not send the label guess for a destination with no stored preset", async () => {
    open(stored({ platform: "custom", url: "rtmps://global-live.mux.com:443/app" }));
    expect(await screen.findByText("Mux Video")).toBeTruthy();
    const body = await save();
    expect("presetId" in body).toBe(false);
  });

  it("sends the preset the operator picks", async () => {
    open(stored({ platform: "custom", url: "rtmps://ingest.example:443/live" }));
    fireEvent.click(await screen.findByRole("button", { name: new RegExp(en["addDest.choosePlatform"]) }));
    fireEvent.click(await screen.findByRole("button", { name: /Instagram Live/ }));
    const body = await save();
    expect(body.presetId).toBe("instagram");
  });

  it("leaves a stored preset alone when nothing is picked", async () => {
    open(stored({ platform: "custom", url: "rtmps://ingest.example:443/live", presetId: "instagram" }));
    const body = await save();
    expect("presetId" in body).toBe(false);
  });
});
