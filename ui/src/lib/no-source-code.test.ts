/// <reference types="node" />
import { afterEach, describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { ApiError, NO_SOURCE, api, isNoSource } from "./api";
import { autoApi } from "./autoApi";

/* The refusal an install with no source answers with, and the ONE thing about
 * it a screen is allowed to branch on.
 *
 * A 503 saying "this install has no source yet" and a 503 saying "the
 * reconcile failed" are the same status. One is an empty state and the other is
 * a fault, and drawing a red toast for the first is exactly the outcome the
 * empty states landing later are meant to replace. The only difference the wire
 * carries is `code`, so a client that reads `message` instead is a client that
 * breaks on a reword -- and every one of these sentences is due to be
 * translated into fifteen languages.
 *
 * Two halves, because either alone passes while the feature is broken: that
 * request() actually lifts the field off the body, and that the string the UI
 * will compare against is the one Go emits.
 *
 * BOTH CLIENTS, and that is the third half. This app has two: lib/api.ts and
 * lib/autoApi.ts, and the second is not a legacy corner -- POST /clips, PUT
 * /clips/buffer, DELETE /clips/{name} and PUT /loudness are reachable from the
 * UI through it and no other way. A contract that holds on one client and not
 * the other reads as satisfied, because the test only ever asked the one.
 */

afterEach(() => {
  vi.unstubAllGlobals();
});

function respondWith(status: number, body: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );
}

describe("the no-source refusal", () => {
  it("arrives on ApiError.code, not only in the sentence", async () => {
    respondWith(503, {
      error: "this install has no source yet, so there is no programme to act on.",
      code: "no_source",
    });
    await expect(api.status()).rejects.toSatisfy(
      (e: unknown) => e instanceof ApiError && e.status === 503 && e.code === "no_source",
    );
  });

  it("leaves code empty for the errors that carry none", async () => {
    respondWith(400, { error: "invalid id" });
    await expect(api.status()).rejects.toSatisfy(
      (e: unknown) => e instanceof ApiError && e.code === "",
    );
  });

  // THE SECOND CLIENT. autoApi is the only route the Clips and Meters pages
  // have to four of the endpoints the guard refuses, and it threw a bare Error
  // -- no status, no code, nothing but the sentence -- while the assertion
  // above went green on lib/api.ts. `instanceof ApiError` is the point of the
  // assertion, not decoration: a client that carries `code` on some other shape
  // still makes every consumer special-case which client it called.
  it("arrives on ApiError.code from the automation client too", async () => {
    // The suite runs on the node environment, and a non-GET reads the CSRF
    // cookie off document before it ever reaches fetch.
    vi.stubGlobal("document", { cookie: "" });
    respondWith(503, {
      error: "this install has no source yet, so there is no programme to act on.",
      code: "no_source",
    });
    await expect(autoApi.post("/clips", { seconds: 30 })).rejects.toSatisfy(
      (e: unknown) => e instanceof ApiError && e.status === 503 && e.code === "no_source",
    );
  });

  it("leaves code empty on the automation client for the errors that carry none", async () => {
    respondWith(400, { error: "invalid id" });
    await expect(autoApi.get("/clips")).rejects.toSatisfy(
      (e: unknown) => e instanceof ApiError && e.status === 400 && e.code === "",
    );
  });

  it("agrees with the constant the server emits", () => {
    // The Go side, read rather than restated. A test that compared "no_source"
    // with "no_source" would pass on the day somebody changed one of them.
    //
    // fileURLToPath rather than new URL(...).pathname: a pathname is
    // percent-encoded, so a checkout under a directory with a space in its name
    // resolved to a path that does not exist and this test failed with ENOENT
    // rather than asserting anything. The reaction to the one test tying these
    // two constants together going red for no reason is to delete it.
    const src = readFileSync(
      join(fileURLToPath(new URL("../../../", import.meta.url)), "internal/api/api.go"),
      "utf8",
    );
    const match = src.match(/codeNoSource\s*=\s*"([^"]+)"/);
    expect(match, "no codeNoSource constant in internal/api/api.go").not.toBeNull();
    expect(match?.[1]).toBe("no_source");
    // And the TypeScript constant the empty states compare against, which is
    // the half that is actually READ at runtime. Four pages branch on it, and
    // a literal repeated four times is four chances to typo it into a
    // comparison that is simply always false -- a failure that presents as the
    // red toast the empty states were added to replace.
    expect(NO_SOURCE, "lib/api.ts and internal/api/api.go disagree about the code")
      .toBe(match?.[1]);
  });

  /* The predicate every empty state is drawn from.
   *
   * Asserted at the function rather than by each page repeating the condition,
   * and the negative cases are the load-bearing ones: a helper that answered
   * true for any 503 would blank the dashboard the first time a reconcile
   * failed, which is the opposite of what the code exists for. */
  it("recognises the refusal and nothing else", () => {
    expect(isNoSource(new ApiError(503, "no programme", NO_SOURCE))).toBe(true);
    expect(isNoSource(new ApiError(503, "reconcile failed"))).toBe(false);
    expect(isNoSource(new ApiError(404, "no such destination"))).toBe(false);
    expect(isNoSource(new Error("network down"))).toBe(false);
    expect(isNoSource(undefined)).toBe(false);
  });
});
