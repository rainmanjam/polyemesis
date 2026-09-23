// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { asDestinationId, asSourceId } from "./types";

// THE CONSOLE SENDS THE CONFIRMATION THE SERVER NOW REQUIRES.
//
// DELETE /sources/{id} refuses without {"confirm": true, "destinations": N}, and
// DELETE /destinations/{id} refuses without {"confirm": true} when the row is
// carrying a broadcast in testing or live. Both deletes sit behind a confirm
// dialog here, so the transport has to carry what that dialog established -- a
// bare DELETE would turn every source delete in the console into a 400.

vi.mock("sonner", () => ({ toast: { warning: vi.fn(), success: vi.fn(), error: vi.fn() } }));

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  vi.resetModules();
  fetchMock = vi.fn(async () => ({ ok: true, status: 204, text: async () => "" }));
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => vi.unstubAllGlobals());

function sentBody(): unknown {
  const init = fetchMock.mock.calls[0][1] as RequestInit;
  expect(init.method).toBe("DELETE");
  return JSON.parse(String(init.body));
}

describe("confirmed deletes", () => {
  it("a source delete carries the confirmation and the destination count it was shown", async () => {
    const { api } = await import("./api");
    await api.deleteSource(asSourceId(4), 3);
    expect(String(fetchMock.mock.calls[0][0])).toContain("/sources/4");
    expect(sentBody()).toEqual({ confirm: true, destinations: 3 });
  });

  it("a destination delete carries the confirmation", async () => {
    const { api } = await import("./api");
    await api.deleteDestination(asDestinationId(9));
    expect(String(fetchMock.mock.calls[0][0])).toContain("/destinations/9");
    expect(sentBody()).toEqual({ confirm: true });
  });
});
