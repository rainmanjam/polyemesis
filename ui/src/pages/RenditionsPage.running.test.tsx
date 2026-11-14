// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* The "N encoding" badge counts the renditions the cards are drawn from. The
 * live list can still hold the rows of a programme that has just been deleted
 * for a few seconds while the page's own list has already dropped its cards,
 * and a count that includes rows with no card is a number nobody can find. */

const running = { state: "running" };

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({
    status: null,
    system: null,
    allRenditions: [
      { id: 1, name: "720p", consumers: 1, process: running },
      // Its programme is gone: no card, still in the live list for a moment.
      { id: 99, name: "gone", consumers: 1, process: running },
    ],
  }),
}));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  const empty = () => Promise.reject(new Error("not in this test"));
  const list = () =>
    Promise.resolve([
      {
        rendition: { id: 1, name: "720p", width: 1280, height: 720, fps: 30, videoBitrate: 3000, encoder: "libx264", preset: "veryfast", gopSeconds: 2, deinterlace: "off", note: "" },
        destinations: 1,
        enabledDestinations: 1,
      },
    ]);
  return {
    ...actual,
    api: new Proxy({ ...actual.api, listRenditions: list } as Record<string, unknown>, {
      get: (target, key: string) => (key === "listRenditions" ? target[key] : empty),
    }),
  };
});

const { RenditionsPage } = await import("./RenditionsPage");

afterEach(cleanup);

describe("RenditionsPage running count", () => {
  it("counts only the renditions that have a card", async () => {
    render(
      <MemoryRouter>
        <RenditionsPage />
      </MemoryRouter>,
    );
    expect(await screen.findByText("1 encoding")).toBeTruthy();
    expect(screen.queryByText("2 encoding")).toBeNull();
  });
});
