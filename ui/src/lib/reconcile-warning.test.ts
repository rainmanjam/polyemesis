// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// A MUTATION THE SERVER SAVED BUT COULD NOT APPLY RAISES A WARNING. #709.
//
// The server used to log a failed reconcile and answer 200, so the dashboard
// raised a GREEN toast for a change that had not taken effect. The worst case
// was a destination delete: the row left the list, the response said "deleted",
// and the FFmpeg child kept publishing to a destination the console no longer
// drew.
//
// This is read in the transport rather than at the call sites, so the assertion
// is about the transport: a body carrying the flag must produce the amber
// toast, and one without it must not.

const toast = { warning: vi.fn(), success: vi.fn(), error: vi.fn() };
vi.mock("sonner", () => ({ toast }));

async function respondWith(status: number, body: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({
      ok: status < 400,
      status,
      text: async () => (body === undefined ? "" : JSON.stringify(body)),
    })),
  );
  const { api } = await import("./api");
  return api;
}

describe("a reconcile the server could not perform", () => {
  beforeEach(() => {
    vi.resetModules();
    toast.warning.mockClear();
  });
  afterEach(() => vi.unstubAllGlobals());

  it("warns, with the server's own sentence", async () => {
    const sentence =
      "the destination delete was saved, but the running pipeline could not be updated to match it";
    const api = await respondWith(200, {
      status: "deleted",
      reconcileFailed: true,
      warnings: [sentence],
    });

    await api.deleteDestination(7 as never);
    await new Promise((r) => setTimeout(r, 0));

    expect(toast.warning).toHaveBeenCalledTimes(1);
    expect(toast.warning.mock.calls[0][0]).toBe(sentence);
  });

  it("says nothing when the reconcile succeeded", async () => {
    const api = await respondWith(200, { status: "deleted" });

    await api.deleteDestination(7 as never);
    await new Promise((r) => setTimeout(r, 0));

    // A warning on every mutation is a warning an operator stops reading, which
    // is the same silence by a different route.
    expect(toast.warning).not.toHaveBeenCalled();
  });

  it("reads the flag, not the array, so an unrelated warning is not doubled", async () => {
    // DestinationDialog renders `warnings` itself. If this warned on the array
    // alone, a platform-settings note would appear twice on every create.
    const api = await respondWith(200, {
      destination: { id: 1 },
      warnings: ["the stream key looks short"],
    });

    await api.createDestination({} as never);
    await new Promise((r) => setTimeout(r, 0));

    expect(toast.warning).not.toHaveBeenCalled();
  });
});
