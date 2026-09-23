/// <reference types="node" />
import { afterEach, describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { ApiError, SETTINGS_CONFLICT, api, isSettingsConflict } from "./api";
import type { Settings } from "./types";

/* A Settings save made from a stale read must be refused, not win.
 *
 * PUT /settings takes the whole document, and every page that saves settings
 * sends the whole document it read. So the last writer won on every field: a
 * tab opened with an auto-ban armed, another operator disarmed it, the first
 * tab saved a retention change, and the ban was armed again.
 *
 * The server now serves a `version` with the document and refuses (409,
 * settings_conflict) a PUT whose version is not the stored one. The half that
 * lives HERE is that the version actually travels: that putSettings sends the
 * one it was given and hands back the new one, rather than stripping it the
 * way it strips `reload`. A page that lost it would silently fall back to
 * last-writer-wins, which is the bug.
 */

afterEach(() => {
  vi.unstubAllGlobals();
});

function respondWith(status: number, body: unknown) {
  const fetchMock = vi.fn(
    async (_url: string, _init?: RequestInit) =>
      new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      }),
  );
  vi.stubGlobal("fetch", fetchMock);
  // A non-GET reads the CSRF cookie off document before it reaches fetch.
  vi.stubGlobal("document", { cookie: "" });
  return fetchMock;
}

describe("the settings version", () => {
  it("is sent with the save and the new one comes back with the result", async () => {
    const fetchMock = respondWith(200, { version: "v2", reload: [] });
    const saved = await api.putSettings({ version: "v1" } as Settings);

    const sent = JSON.parse(String(fetchMock.mock.calls[0][1]?.body));
    expect(sent.version, "the save did not carry the version it was read at").toBe("v1");
    expect(saved.version, "the next save from this page would be unchecked").toBe("v2");
    expect("reload" in saved).toBe(false);
  });

  it("refuses as a conflict a caller can branch on", async () => {
    respondWith(409, { error: "changed by someone else", code: "settings_conflict" });
    const err = await api.putSettings({ version: "v1" } as Settings).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(isSettingsConflict(err)).toBe(true);
    expect(isSettingsConflict(new ApiError(409, "other", "account_in_use"))).toBe(false);
  });

  /* A save that the server STORED and then refused -- no_source for the
   * ingest half of a first-time operator's save, or a failed reconcile -- comes
   * back as an error carrying the version now stored. The page holds the
   * document it read and never sees that version, so without this its next
   * save sent the old one and was refused as "changed by someone else": a
   * conflict with the operator's own change. putSettings carries it forward
   * itself, so no page can forget to. */
  it("follows the version an error-after-store handed back, so a retry does not conflict with itself", async () => {
    respondWith(503, { error: "no source", code: "no_source", version: "v2" });
    const err = await api.putSettings({ version: "v1" } as Settings).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);

    const fetchMock = respondWith(200, { version: "v3", reload: [] });
    await api.putSettings({ version: "v1" } as Settings);
    const sent = JSON.parse(String(fetchMock.mock.calls[0][1]?.body));
    expect(sent.version, "the retry sent the version the page read, which the server has since replaced with its own save").toBe("v2");
  });

  it("does not move a version forward for an error that stored nothing", async () => {
    respondWith(409, { error: "changed by someone else", code: "settings_conflict" });
    await api.putSettings({ version: "w1" } as Settings).catch(() => undefined);

    const fetchMock = respondWith(200, { version: "w2", reload: [] });
    await api.putSettings({ version: "w1" } as Settings);
    const sent = JSON.parse(String(fetchMock.mock.calls[0][1]?.body));
    expect(sent.version).toBe("w1");
  });

  it("agrees with the constant the server emits", () => {
    const src = readFileSync(
      join(fileURLToPath(new URL("../../../", import.meta.url)), "internal/api/api.go"),
      "utf8",
    );
    const match = src.match(/codeSettingsConflict\s*=\s*"([^"]+)"/);
    expect(match, "no codeSettingsConflict constant in internal/api/api.go").not.toBeNull();
    expect(SETTINGS_CONFLICT).toBe(match?.[1]);
  });
});
