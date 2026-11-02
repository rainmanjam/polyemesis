// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { asDestinationId, asSourceId } from "./types";

// THE DUMP'S BODY IS THE CHOICE THE CONFIRM DIALOG MADE.
//
// No body means "everything since the last dump point"; {"seconds": N} means
// at least the newest N s. Sending {"seconds": undefined} or {"seconds": 0}
// for the first would happen to mean the same thing today, but the server
// documents the absent body, so that is what the console sends. Dump-all
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
  it("sends no body for everything since the last dump point", async () => {
    const { api } = await import("./api");
    await api.dumpDestinationDelay(asDestinationId(7));
    const { url, body } = sent();
    expect(url).toContain("/destinations/7/delay/dump");
    expect(body).toBeUndefined();
  });

  it("sends the number of seconds when one was chosen", async () => {
    const { api } = await import("./api");
    await api.dumpDestinationDelay(asDestinationId(7), 12);
    expect(JSON.parse(String(sent().body))).toEqual({ seconds: 12 });
  });

  it("dumps every delayed destination of one programme, confirmed", async () => {
    const { api } = await import("./api");
    await api.dumpAllDelays(asSourceId(3));
    const { url, body } = sent();
    expect(url).toContain("/destinations/delay/dump-all?source=3");
    expect(JSON.parse(String(body))).toEqual({ confirm: true });
  });

  it("carries the seconds on a dump-all too", async () => {
    const { api } = await import("./api");
    await api.dumpAllDelays(asSourceId(3), 10);
    expect(JSON.parse(String(sent().body))).toEqual({ confirm: true, seconds: 10 });
  });
});
