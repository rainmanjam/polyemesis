// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { asDestinationId } from "./types";

// THE DUMP'S BODY IS THE CHOICE THE CONFIRM DIALOG MADE.
//
// No body means "this destination's own dump default" (which is "everything
// since the last dump point" when it has none); {"seconds": N} overrides it,
// and {"seconds": 0} is "everything since the last dump point" even on a
// destination with a default. So 0 has to travel: dropping it as falsy would
// quietly turn the operator's choice into the stored default. Dump-all
// carries the confirmation the server refuses it without, scoped to one
// programme like stop-all.

vi.mock("sonner", () => ({ toast: { warning: vi.fn(), success: vi.fn(), error: vi.fn() } }));

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  vi.resetModules();
  fetchMock = vi.fn(async () => ({ ok: true, status: 200, text: async () => "{}" }));
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => vi.unstubAllGlobals());

const sent = () => {
  const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
  expect(init.method).toBe("POST");
  return { url: String(url), body: init.body };
};

describe("dumping a destination's delay", () => {
  it("sends no body when no seconds are given, leaving the destination's default", async () => {
    const { api } = await import("./api");
    await api.dumpDestinationDelay(asDestinationId(7));
    const { url, body } = sent();
    expect(url).toContain("/destinations/7/delay/dump");
    expect(body).toBeUndefined();
  });

  it("sends an explicit 0, which overrides the destination's default", async () => {
    const { api } = await import("./api");
    await api.dumpDestinationDelay(asDestinationId(7), 0);
    expect(JSON.parse(String(sent().body))).toEqual({ seconds: 0 });
  });

  it("sends the number of seconds when one was chosen", async () => {
    const { api } = await import("./api");
    await api.dumpDestinationDelay(asDestinationId(7), 12);
    expect(JSON.parse(String(sent().body))).toEqual({ seconds: 12 });
  });
});
