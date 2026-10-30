// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { api } from "./api";
import type { DestinationId } from "./types";

const D7 = 7 as unknown as DestinationId;

/* I4: the routing page's live mute and gain go to the per-track route, named by
 * programme, as a POST carrying only the field that changed. */

const realFetch = globalThis.fetch;
let calls: Array<[string, RequestInit]>;

beforeEach(() => {
  document.cookie = "polyemesis_csrf=tok-abc123";
  calls = [];
  globalThis.fetch = (async (url: string, init: RequestInit) => {
    calls.push([url, init]);
    return {
      ok: true,
      status: 200,
      headers: new Headers(),
      text: async () => JSON.stringify({ applied: "live", destination: { id: 7 } }),
    } as unknown as Response;
  }) as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
});

describe("api.setTrackLevel", () => {
  it("POSTs one track's mute to the gain route of that destination's programme", async () => {
    const res = await api.setTrackLevel(D7, 2, { muted: true }, 3);
    expect(res.applied).toBe("live");
    const [url, init] = calls[0];
    expect(url).toMatch(/\/destinations\/7\/tracks\/2\/gain\?source=3$/);
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ muted: true });
  });

  it("names no programme for a row that predates sources", async () => {
    await api.setTrackLevel(D7, 0, { gain: 0.5 }, null);
    expect(calls[0][0]).toMatch(/\/destinations\/7\/tracks\/0\/gain$/);
    expect(JSON.parse(String(calls[0][1].body))).toEqual({ gain: 0.5 });
  });
});
