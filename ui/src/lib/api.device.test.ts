// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError, api } from "./api";

/* THE TWO CALLS THAT CONNECT AN ACCOUNT FROM A BOX NOTHING CAN REDIRECT BACK TO.
 *
 * They are one-liners over the shared request() helper, so what is worth
 * asserting is not the helper -- it is the two facts a one-liner can get wrong
 * silently, both of which cost the operator the code they are already typing:
 *
 *   a poll that goes to the START endpoint issues a NEW code while the operator
 *   is halfway through entering the old one, and the platform then rejects what
 *   they typed. Nothing on either screen says why.
 *
 *   a poll that loses the handle redeems nothing. The server answers "no longer
 *   being tracked", which reads to the operator as an expired code, and the
 *   dialog tells them to start again -- forever.
 */

const realFetch = globalThis.fetch;

function respond(body: unknown, status = 200): Response {
  return {
    ok: status < 400,
    status,
    headers: new Headers(),
    text: async () => JSON.stringify(body),
  } as unknown as Response;
}

let calls: Array<[string, RequestInit]>;

beforeEach(() => {
  document.cookie = "polyemesis_csrf=tok-abc123";
  calls = [];
});

afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

function capture(...responses: Response[]): void {
  let i = 0;
  globalThis.fetch = (async (url: string, init: RequestInit) => {
    calls.push([url, init]);
    return responses[Math.min(i++, responses.length - 1)];
  }) as unknown as typeof fetch;
}

describe("api.startDeviceAuth", () => {
  it("asks the platform for a code and hands back what it said", async () => {
    capture(
      respond({
        handle: "h-1",
        userCode: "ABCD-1234",
        verificationUri: "https://www.twitch.tv/activate?public=true",
        expiresAt: "2026-08-19T12:30:00Z",
        intervalSeconds: 5,
      }),
    );

    const auth = await api.startDeviceAuth("twitch");

    expect(auth.userCode).toBe("ABCD-1234");
    // Passed through verbatim, query string included. The activation address is
    // the platform's to choose and a client that rebuilt it from a remembered
    // hostname would send the operator to a page that does not know their code.
    expect(auth.verificationUri).toBe("https://www.twitch.tv/activate?public=true");
    const [url, init] = calls[0];
    expect(url).toBe("/api/v1/platforms/credentials/twitch/device");
    // A POST because it makes an outbound call to the platform on the
    // operator's own developer app -- so it is CSRF-covered, and a browser
    // prefetching a link cannot burn a code.
    expect(init.method).toBe("POST");
    expect((init.headers as Headers).get("X-CSRF-Token")).toBe("tok-abc123");
  });

  it("raises the server's own sentence when the platform has no device flow", async () => {
    // The two cases an operator can actually fix -- no credentials stored, or a
    // platform with no device flow at all -- have their fix IN the server's
    // sentence, and it names the platform. The dialog shows it verbatim.
    // Verbatim from deviceUnsupportedReason in internal/api/device_flow.go: a
    // paraphrase here would let this test go on passing across a change to the
    // sentence the operator actually reads.
    capture(
      respond(
        {
          error:
            "kick has no device authorization flow polyemesis can use; " +
            "connect this account with the ordinary Connect button instead",
        },
        400,
      ),
    );

    await expect(api.startDeviceAuth("kick")).rejects.toThrow(
      /kick has no device authorization flow polyemesis can use/,
    );
    await expect(api.startDeviceAuth("kick")).rejects.toBeInstanceOf(ApiError);
  });
});

describe("api.pollDeviceAuth", () => {
  it("redeems the handle it was given, at the poll endpoint", async () => {
    capture(respond({ state: "pending", retryInSeconds: 5 }));

    const res = await api.pollDeviceAuth("twitch", "h-1");

    expect(res.state).toBe("pending");
    const [url, init] = calls[0];
    // NOT the start endpoint. Polling the starter would mint a second code
    // behind the operator while they are entering the first.
    expect(url).toBe("/api/v1/platforms/credentials/twitch/device/poll");
    expect(init.method).toBe("POST");
    // The handle is the whole identity of the flow. Sent, and sent in the body
    // rather than the path, because a poll is not a page and the handle has no
    // business in an access log.
    expect(JSON.parse(init.body as string)).toEqual({ handle: "h-1" });
  });

  it("keeps the two endpoints apart across a start and a poll of one flow", async () => {
    capture(
      respond({
        handle: "h-9",
        userCode: "WXYZ-9876",
        verificationUri: "https://www.twitch.tv/activate",
        expiresAt: "2026-08-19T12:30:00Z",
        intervalSeconds: 5,
      }),
      respond({ state: "connected", account: { accountName: "Dallas" } }),
    );

    const auth = await api.startDeviceAuth("twitch");
    const res = await api.pollDeviceAuth("twitch", auth.handle);

    expect(res.state).toBe("connected");
    expect(calls.map(([u]) => u)).toEqual([
      "/api/v1/platforms/credentials/twitch/device",
      "/api/v1/platforms/credentials/twitch/device/poll",
    ]);
    // The handle the start returned is the one the poll redeems. A poll that
    // invented or dropped it is answered "no longer being tracked", which the
    // dialog can only report as an expired code.
    expect(JSON.parse(calls[1][1].body as string)).toEqual({ handle: "h-9" });
  });
});
