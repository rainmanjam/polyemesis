/// <reference types="node" />
import { afterEach, describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { ApiError, api } from "./api";

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

  it("agrees with the constant the server emits", () => {
    // The Go side, read rather than restated. A test that compared "no_source"
    // with "no_source" would pass on the day somebody changed one of them.
    const src = readFileSync(
      join(new URL("../../../", import.meta.url).pathname, "internal/api/api.go"),
      "utf8",
    );
    const match = src.match(/codeNoSource\s*=\s*"([^"]+)"/);
    expect(match, "no codeNoSource constant in internal/api/api.go").not.toBeNull();
    expect(match?.[1]).toBe("no_source");
  });
});
