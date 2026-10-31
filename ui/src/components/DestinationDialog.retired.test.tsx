// @vitest-environment jsdom

import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* A RETIRED DESTINATION STAYS RETIRED THROUGH A SAVE.
 *
 * Trovo shut down on 30 June 2026 and its preset is gone, so reopening a Trovo
 * destination matches no catalogue entry. If the dialog then fell back to a
 * preset's platform -- custom -- a plain rename would quietly un-retire the
 * row: the card's shutdown notice would vanish and Start would be offered
 * against an ingest that no longer exists. The platform has to come back out
 * exactly as it went in.
 */

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
      listServices: () => Promise.resolve({ services: [] }),
      services: () => Promise.resolve({}),
      updateDestination: (id: unknown, body: unknown) => updateDestination(id, body),
    },
  };
});

import { DestinationDialog } from "./DestinationDialog";

const oldTrovo = {
  id: 7,
  name: "Old Trovo channel",
  kind: "rtmp",
  platform: "trovo",
  enabled: false,
  retired: "Trovo shut down on 30 June 2026",
  url: "rtmp://livepush.trovo.example/live",
  streamKey: "SENTINEL-ui-retired-91c2",
  audioBitrate: 160,
  sourceId: 1,
};

beforeAll(() => {
  (window as unknown as { ResizeObserver?: unknown }).ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.releasePointerCapture = () => {};
  window.HTMLElement.prototype.setPointerCapture = () => {};
  window.HTMLElement.prototype.scrollIntoView = () => {};
});

describe("editing a destination on a retired platform", () => {
  beforeEach(() => updateDestination.mockReset().mockResolvedValue({ warnings: [] }));
  afterEach(cleanup);

  it("saves it with its platform unchanged", async () => {
    render(
      <DestinationDialog open onOpenChange={() => {}} destination={oldTrovo as never} onSaved={() => {}} />,
      { wrapper: MemoryRouter },
    );
    const name = screen.getByDisplayValue("Old Trovo channel");
    fireEvent.change(name, { target: { value: "Archived Trovo channel" } });
    // Save lives on step 2 of the dialog (A6), as in the stream-key test.
    // Until the source list has loaded Save sends the operator back to step 1
    // instead, so press it again until it goes through, the way an operator
    // on a slow link would.
    await waitFor(() => {
      fireEvent.click(screen.getByTestId("dest-step-2"));
      fireEvent.click(screen.getByRole("button", { name: /^Save$/ }));
      expect(updateDestination).toHaveBeenCalled();
    });
    const body = updateDestination.mock.calls[0][1] as Record<string, unknown>;
    expect(body.name).toBe("Archived Trovo channel");
    expect(body.platform).toBe("trovo");
  });
});
