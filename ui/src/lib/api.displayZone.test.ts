// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { api } from "./api";
import { displayTimeZone, setDisplayTimeZone } from "./format";

/* The display zone follows the settings on every read and every save, so a
 * zone changed from another browser or an API client is picked up the next time
 * this console fetches settings. */

const realFetch = globalThis.fetch;
const fetchMock = vi.fn();

function reply(tz: string): Response {
  return new Response(JSON.stringify({ display: { timeZone: tz } }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

beforeEach(() => {
  globalThis.fetch = fetchMock as unknown as typeof fetch;
});
afterEach(() => {
  globalThis.fetch = realFetch;
  fetchMock.mockReset();
  setDisplayTimeZone("UTC");
});

describe("the display zone and the settings API", () => {
  it("is refreshed by a fresh settings read", async () => {
    setDisplayTimeZone("UTC");
    fetchMock.mockResolvedValue(reply("Asia/Tokyo"));
    await api.getSettings();
    expect(displayTimeZone()).toBe("Asia/Tokyo");

    fetchMock.mockResolvedValue(reply("Europe/London")); // changed elsewhere
    await api.getSettings();
    expect(displayTimeZone()).toBe("Europe/London");
  });

  it("is set by a save", async () => {
    fetchMock.mockResolvedValue(reply("Australia/Sydney"));
    await api.putSettings({ display: { timeZone: "Australia/Sydney" } } as never);
    expect(displayTimeZone()).toBe("Australia/Sydney");
  });
});
