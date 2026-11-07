// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { WIRE } from "./contract/wire";

/* api.twitchTool is one path, /chat/twitch/{resource}, and the method is the
 * verb. Reads and deletes carry their parameters in the query, with the account
 * last; the rest carry a JSON body. */

function fakeFetch() {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    return new Response("{}", { status: 200, headers: { "content-type": "application/json" } });
  });
  return calls;
}
afterEach(() => vi.unstubAllGlobals());

describe("api.twitchTool", () => {
  it("builds the path and verb for each call", async () => {
    const calls = fakeFetch();
    await api.twitchTool.read("roles", "acct", { role: "vip" });
    await api.twitchTool.read("banned", undefined);
    await api.twitchTool.post("announcements", "acct", { message: "hi" });
    await api.twitchTool.put("shield", "acct", { active: true });
    await api.twitchTool.patch("pin", "acct", { messageId: "m" });
    await api.twitchTool.remove("blocked-terms", "acct", { id: "i 1" });
    const got = calls.map((c) => `${c.init?.method ?? "GET"} ${c.url.replace(/^.*\/api\/v1/, "")}`);
    expect(got).toEqual([
      "GET /chat/twitch/roles?role=vip&account=acct",
      "GET /chat/twitch/banned",
      "POST /chat/twitch/announcements?account=acct",
      "PUT /chat/twitch/shield?account=acct",
      "PATCH /chat/twitch/pin?account=acct",
      "DELETE /chat/twitch/blocked-terms?id=i+1&account=acct",
    ]);
    expect(JSON.parse(String(calls[2].init?.body))).toEqual({ message: "hi" });
  });
});

describe("the channel tools' limits", () => {
  it("come from the server's wire contract", () => {
    expect(WIRE.twitchTools).toEqual({
      maxAnnouncement: 500, maxWarning: 500, minTerm: 2, maxTerm: 500, minPinSeconds: 30, maxPinSeconds: 1800,
    });
  });
});
