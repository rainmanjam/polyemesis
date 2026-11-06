// @vitest-environment jsdom

/* autoApi used to be a second HTTP client that had drifted from lib/api.ts.
 * These pin the behaviours it lacked, through the client the clip, automation
 * and recording pages actually call, so the two can never part again. */

import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, http } from "./api";

const toast = vi.hoisted(() => ({ warning: vi.fn() }));
vi.mock("sonner", () => ({ toast }));

function reply(body: string, status: number) {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(body, { status })));
}

afterEach(() => {
  vi.unstubAllGlobals();
  toast.warning.mockReset();
});

describe("autoApi error handling matches lib/api.ts", () => {
  it("quotes a plain-text error body", async () => {
    reply("clip window too wide", 400);
    await expect(http.get("/x")).rejects.toMatchObject({ message: "clip window too wide" });
  });

  it("does not quote an HTML error page", async () => {
    reply("<html><body>502 Bad Gateway</body></html>", 502);
    await expect(http.get("/x")).rejects.toMatchObject({ message: "request failed (502)" });
  });

  it("keeps the stored version of a settings conflict", async () => {
    reply(JSON.stringify({ error: "conflict", version: "7" }), 409);
    const err = (await http.put("/x", {}).catch((e: unknown) => e)) as ApiError;
    expect(err.storedVersion).toBe("7");
  });

  it("raises the amber toast for a saved-but-not-applied change", async () => {
    reply(JSON.stringify({ reconcileFailed: true, warnings: ["pipeline not updated"] }), 200);
    await http.post("/x", { a: 1 });
    await vi.waitFor(() => expect(toast.warning).toHaveBeenCalledWith("pipeline not updated", { duration: 15000 }));
  });
});

describe("requestRaw does not label a non-JSON body as JSON", () => {
  it("leaves a FormData body's content type to the browser", async () => {
    const { requestRaw } = await import("./api");
    const fetchMock = vi.fn(async () => new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    await requestRaw("/media", { method: "POST", body: new FormData() });
    const init = (fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1];
    expect(new Headers(init.headers).has("Content-Type")).toBe(false);
  });
});
