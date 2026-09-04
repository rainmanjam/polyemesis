// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";

/* #661: THE WARNING BELONGS WHERE THE RENDITION IS CHOSEN.
 *
 * db.RenditionConcerns shipped correct and unreachable. Its only caller logged
 * at stream start, so attaching a 4K60 rendition at 40 Mbps to an X destination
 * showed nothing in the console and produced a log line after the operator had
 * already committed — the issue asked for a warning "at the point of choosing",
 * and that is the half that was missing.
 *
 * These tests pin the dialog end of it. The comparison itself is the server's
 * and is tested there; what is pinned here is that the console ASKS, with the
 * platform the operator has chosen, and SHOWS what comes back — including the
 * source and the date, because the catalogue is a snapshot of someone else's
 * documentation and can be the stale half.
 */

const renditionConcerns = vi.fn();

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      listSources: () => Promise.resolve([{ id: 1, name: "Main", publishUrls: {}, isDefault: true }]),
      listAccounts: () => Promise.resolve([]),
      listPlatformPresets: () => Promise.resolve([]),
      listServices: () => Promise.resolve({ services: [] }),
      services: () => Promise.resolve({}),
      listRenditions: () =>
        Promise.resolve([
          {
            rendition: {
              id: 7,
              name: "far too much",
              width: 3840,
              height: 2160,
              fps: 60,
              videoBitrate: 40000,
              gopSeconds: 2,
            },
            destinationCount: 1,
          },
        ]),
      renditionConcerns: (id: number, platform: string) => renditionConcerns(id, platform),
    },
  };
});

import { DestinationDialog } from "./DestinationDialog";

const onX = {
  id: 9,
  name: "X",
  kind: "rtmp",
  platform: "x",
  url: "rtmp://ingest.example/live",
  audioBitrate: 160,
  sourceId: 1,
  renditionId: 7,
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

afterEach(() => {
  cleanup();
  renditionConcerns.mockReset();
});

function open(destination: unknown) {
  return render(
    <DestinationDialog
      open
      onOpenChange={() => {}}
      destination={destination as never}
      onSaved={() => {}}
    />,
  );
}

describe("the rendition warning", () => {
  it("asks the server about the chosen rendition AND the chosen platform", async () => {
    renditionConcerns.mockResolvedValue([]);
    open(onX);
    // The pair is what is being judged: a platform switch with the rendition
    // unchanged is exactly the case this exists for, so both must be sent.
    await waitFor(() => expect(renditionConcerns).toHaveBeenCalledWith(7, "x"));
  });

  it("shows the objection, and the date the figure was checked", async () => {
    renditionConcerns.mockResolvedValue([
      {
        field: "bitrate",
        detail: "X publishes exactly 12000 kbps; this rendition sends 40000",
        source: "https://help.x.com/live",
        checked: "2026-08-06",
      },
    ]);
    open(onX);

    await screen.findByTestId("rendition-concerns");
    expect(screen.getByText(/publishes exactly 12000 kbps/)).toBeTruthy();

    // The date is a link to the source. #661 asks for both so an operator can
    // judge WHICH is stale — the catalogue or their choice — rather than being
    // told they are wrong by a number with no provenance.
    const link = screen.getByRole("link", { name: "2026-08-06" });
    expect(link.getAttribute("href")).toBe("https://help.x.com/live");
  });

  it("re-asks when the PLATFORM changes and the rendition does not", async () => {
    // The case the warning exists for: an operator moves a working encode to a
    // destination that will not take it. The rendition is unchanged, so a hook
    // that watched only the rendition would keep showing the old verdict --
    // and this test is here because removing `platform` from the dependency
    // array broke nothing until it was written.
    renditionConcerns.mockResolvedValue([]);
    const { rerender } = open(onX);
    await waitFor(() => expect(renditionConcerns).toHaveBeenCalledWith(7, "x"));

    rerender(
      <DestinationDialog
        open
        onOpenChange={() => {}}
        destination={{ ...onX, platform: "youtube" } as never}
        onSaved={() => {}}
      />,
    );
    await waitFor(() => expect(renditionConcerns).toHaveBeenCalledWith(7, "youtube"));
  });

  it("says nothing when the rendition suits the platform", async () => {
    renditionConcerns.mockResolvedValue([]);
    open(onX);
    await waitFor(() => expect(renditionConcerns).toHaveBeenCalled());
    expect(screen.queryByTestId("rendition-concerns")).toBeNull();
  });

  it("shows nothing rather than something stale when the request fails", async () => {
    // A concern shown against a platform the operator has already moved away
    // from is worse than silence: it is confidently about the wrong thing.
    renditionConcerns.mockRejectedValue(new Error("network"));
    open(onX);
    await waitFor(() => expect(renditionConcerns).toHaveBeenCalled());
    expect(screen.queryByTestId("rendition-concerns")).toBeNull();
  });
});
