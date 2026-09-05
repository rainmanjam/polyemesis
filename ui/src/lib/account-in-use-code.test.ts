/// <reference types="node" />
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { ACCOUNT_IN_USE, ApiError, api, isAccountInUse } from "./api";

/* The refusal a disconnect gets when the account is still carrying
 * destinations, and the three things a client needs in order to answer it.
 *
 * WHY THIS IS NOT AN EDGE CASE. The server's `blocks()` fires on `Enabled`
 * alone, and a normal install leaves its destinations enabled -- so the 409 is
 * the ORDINARY answer to pressing Disconnect, not a rare one. A client with no
 * branch for it does not degrade gracefully; its Disconnect button stops
 * working for most accounts, silently, because the rejection has nowhere to go.
 *
 * Four halves, because each one alone passes while the feature is broken:
 *
 *   1. `code` arrives, so a screen can branch without reading English that is
 *      translated into fifteen languages.
 *   2. `destinations` arrives, so the screen can say WHICH -- a refusal an
 *      operator cannot act on is a dead end with better wording.
 *   3. The TypeScript constant equals the Go one. A test comparing
 *      "account_in_use" with "account_in_use" passes on the day one of them
 *      changes.
 *   4. The confirmed re-send actually carries a body. `del` takes no body at
 *      all, so `deleteAccount` could not have sent {"confirm": true} however
 *      the call site was written -- which is what made the loop unbreakable
 *      rather than merely ugly.
 */

/** The 409 body internal/api/oauth_handlers.go writes, field for field. */
const REFUSAL = {
  error:
    "2 destinations are still on this connected account: Main YouTube, Backup. " +
    'Send this request again with {"confirm": true} to do it anyway.',
  code: "account_in_use",
  destinations: [
    {
      id: 1,
      name: "Main YouTube",
      platform: "youtube",
      enabled: true,
      broadcastId: "abc",
      phase: "live",
      broadcasting: true,
    },
    { id: 2, name: "Backup", platform: "youtube", enabled: true, broadcasting: false },
  ],
};

let calls: { url: string; init: RequestInit }[] = [];

function respondWith(status: number, body: unknown) {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit = {}) => {
      calls.push({ url, init });
      return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
}

beforeEach(() => {
  // The suite runs on the node environment, and a non-GET reads the CSRF
  // cookie off document before it ever reaches fetch.
  vi.stubGlobal("document", { cookie: "" });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("the account-in-use refusal", () => {
  it("arrives with its code and the rows it is about", async () => {
    respondWith(409, REFUSAL);
    const err = await api.deleteAccount(7).then(
      () => null,
      (e: unknown) => e,
    );

    expect(err).toBeInstanceOf(ApiError);
    const api409 = err as ApiError;
    expect(api409.status).toBe(409);
    expect(api409.code).toBe(ACCOUNT_IN_USE);
    expect(isAccountInUse(api409)).toBe(true);

    // WHICH destinations. A count would have been satisfied by `length`; the
    // names are the thing an operator acts on.
    expect(api409.destinations.map((d) => d.name)).toEqual(["Main YouTube", "Backup"]);
    // And which of them is mid-broadcast, which is the one that cannot be
    // undone from inside this product afterwards.
    expect(api409.destinations.filter((d) => d.broadcasting).map((d) => d.name)).toEqual([
      "Main YouTube",
    ]);
  });

  it("leaves destinations empty for the errors that name none", async () => {
    respondWith(400, { error: "invalid id" });
    const err = (await api.deleteAccount(7).catch((e: unknown) => e)) as ApiError;
    // [] rather than undefined: a .map on the error path must not be the thing
    // that throws.
    expect(err.destinations).toEqual([]);
    expect(isAccountInUse(err)).toBe(false);
  });

  it("survives a destinations field that is not a list", async () => {
    // Not hypothetical enough to ignore: a proxy or a captive portal can answer
    // any request with a body of its own, and a `.map` over a string produces
    // an exception inside the error handler -- i.e. the refusal branch fails in
    // the one place there is nothing left to catch it.
    respondWith(409, { error: "no", code: "account_in_use", destinations: "Main YouTube" });
    const err = (await api.deleteAccount(7).catch((e: unknown) => e)) as ApiError;
    expect(err.destinations).toEqual([]);
    // Still recognised, so the screen still explains itself rather than
    // reporting a generic fault.
    expect(isAccountInUse(err)).toBe(true);
  });

  it("agrees with the constant the server emits", () => {
    // The Go side, read rather than restated.
    const src = readFileSync(
      join(
        fileURLToPath(new URL("../../../", import.meta.url)),
        "internal/api/oauth_handlers.go",
      ),
      "utf8",
    );
    const match = src.match(/codeAccountInUse\s*=\s*"([^"]+)"/);
    expect(match, "no codeAccountInUse constant in internal/api/oauth_handlers.go").not.toBeNull();
    expect(ACCOUNT_IN_USE, "lib/api.ts and internal/api/oauth_handlers.go disagree").toBe(
      match?.[1],
    );
  });

  it("recognises the refusal and nothing else", () => {
    expect(isAccountInUse(new ApiError(409, "in use", ACCOUNT_IN_USE))).toBe(true);
    // A 409 is not on its own the refusal -- other routes answer 409 for
    // reasons of their own, and treating them all as this one would put a
    // "disconnect anyway" dialog in front of an unrelated failure.
    expect(isAccountInUse(new ApiError(409, "already running"))).toBe(false);
    expect(isAccountInUse(new ApiError(503, "no programme", "no_source"))).toBe(false);
    expect(isAccountInUse(new Error("network down"))).toBe(false);
    expect(isAccountInUse(undefined)).toBe(false);
  });
});

describe("the two ways to disconnect", () => {
  it("sends no body unconfirmed, so the guard is the one that decides", async () => {
    respondWith(200, { status: "disconnected" });
    await api.deleteAccount(7);
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe("/api/v1/platforms/accounts/7");
    expect(calls[0].init.method).toBe("DELETE");
    // Absent, not `{"confirm": false}`. The server reads an absent body as
    // unconfirmed, and a client that always sent a body would make every
    // existing no-body caller the odd one out.
    expect(calls[0].init.body).toBeUndefined();
  });

  it("sends {\"confirm\": true} on the override", async () => {
    respondWith(200, { status: "disconnected", warnings: ["2 destinations are no longer linked"] });
    await api.confirmDeleteAccount(7);
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe("/api/v1/platforms/accounts/7");
    expect(calls[0].init.method).toBe("DELETE");
    // The exact bytes, because DisallowUnknownFields on the Go side turns a
    // misspelt field into a 400 rather than a silent false -- and a test that
    // only checked "a body was sent" would pass on `{"confirmed": true}`.
    expect(JSON.parse(String(calls[0].init.body))).toEqual({ confirm: true });
    expect(new Headers(calls[0].init.headers).get("Content-Type")).toBe("application/json");
  });
});
