import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import type { BeaconV1 } from "./types";

const beacon: BeaconV1 = { v: 1, sid: "ab".repeat(16), seq: 1, kind: "heartbeat", rebufferCount: 0, rebufferMs: 0 };

afterEach(() => vi.unstubAllGlobals());

describe("api.playoutBeacon", () => {
  it("posts the beacon with the token in a header, never in the URL, and outlives the page", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    expect(await api.playoutBeacon(beacon, "SECRET-TOKEN")).toBe(true);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/playout/beacon");
    expect(url).not.toContain("SECRET");
    expect(init.method).toBe("POST");
    expect(init.keepalive).toBe(true);
    expect(init.credentials).toBe("omit");
    expect((init.headers as Record<string, string>)["X-Playout-Token"]).toBe("SECRET-TOKEN");
    expect(JSON.parse(init.body as string)).toEqual(beacon);
  });

  it("sends no token header on an open stream and swallows a failure", async () => {
    const fetchMock = vi.fn().mockRejectedValue(new TypeError("offline"));
    vi.stubGlobal("fetch", fetchMock);
    expect(await api.playoutBeacon(beacon)).toBe(false);
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect((init.headers as Record<string, string>)["X-Playout-Token"]).toBeUndefined();
  });
});
