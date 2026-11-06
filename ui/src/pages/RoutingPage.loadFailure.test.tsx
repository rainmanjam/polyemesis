// @vitest-environment jsdom

import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, render, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

import { RoutingPage } from "./RoutingPage";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import { api } from "@/lib/api";
import { autoApi } from "@/lib/autoApi";

/* The load chain ends in a .catch that toasts, so a failed destination list is
 * said out loud and never left as an unhandled rejection. */

const toastError = vi.hoisted(() => vi.fn());
vi.mock("sonner", () => ({ toast: { error: toastError, success: () => {}, info: () => {} } }));

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  toastError.mockClear();
});
beforeAll(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
});
afterAll(() => vi.unstubAllGlobals());

const live = {
  programme: 3,
  programmeKnown: true,
  programmes: [{ id: 3, name: "Main" }],
  connected: true,
  status: null,
  source: null,
  levels: null,
  system: null,
  bitrate: [],
  logs: [],
} as unknown as LiveData;

describe("RoutingPage destination load", () => {
  it("toasts the error when the destination list cannot be loaded", async () => {
    vi.spyOn(autoApi, "loudness").mockResolvedValue({ enabled: false, reports: [] } as never);
    vi.spyOn(api, "listDestinations").mockRejectedValue(new Error("boom"));
    vi.spyOn(api, "listPresets").mockResolvedValue({ presets: [], defaults: {} } as never);
    render(
      <LiveDataContext.Provider value={live}>
        <TooltipProvider>
          <MemoryRouter initialEntries={["/routing"]}>
            <Routes>
              <Route path="/routing" element={<RoutingPage />} />
            </Routes>
          </MemoryRouter>
        </TooltipProvider>
      </LiveDataContext.Provider>,
    );
    await waitFor(() => expect(toastError).toHaveBeenCalledWith("boom"));
  });
});
