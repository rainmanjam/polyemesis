/* ===========================================================================
   The automation endpoints: alert rules and schedules.

   These are typed and called here rather than from lib/api.ts because that
   module belonged to another workstream when this was written. The two are
   byte-compatible on purpose, so folding this into lib/api.ts stays a cut and
   paste rather than a rewrite.

   It moved out of AutomationPage.tsx because four other pages import `autoApi`
   from it -- ClipsPage, RecordingsPage, ClipEditor and MetersPage -- which made
   a page module the app's second API client. It also cost the automation page
   its Fast Refresh: a file exporting a component next to a plain object cannot
   be hot-swapped, so editing a rule form used to reload the page and throw away
   the half-built alert rule in it.
   =========================================================================== */

import { ApiError } from "./api";

const BASE = "/api/v1";

function csrfToken(): string {
  const match = document.cookie.match(/(?:^|;\s*)polyemesis_csrf=([^;]+)/);
  return match ? decodeURIComponent(match[1]) : "";
}

/** Same contract as lib/api.ts's request: JSON in, JSON out, and the SAME
 *  ApiError on failure. Kept byte-compatible so moving it later is a cut and
 *  paste.
 *
 *  The failure path used to throw a bare Error carrying the sentence and
 *  nothing else, which was byte-compatible with an older lib/api.ts and stopped
 *  being so the moment ApiError grew `code`. Four of the routes the zero-source
 *  guard refuses -- POST /clips, PUT /clips/buffer, DELETE /clips/{name} and
 *  PUT /loudness -- are reachable from the UI ONLY through this client, so a
 *  screen wanting to tell "this install has no programme yet" apart from "the
 *  server is broken" had no field to read and would have had to match on the
 *  English. That is the one thing the whole code contract exists to forbid, and
 *  a second HTTP client that quietly opts out of it is worse than no contract,
 *  because the contract's own test passes. */
async function autoRequest<T>(path: string, init: RequestInit = {}): Promise<T> {
  const method = (init.method ?? "GET").toUpperCase();
  const headers = new Headers(init.headers);
  if (init.body) headers.set("Content-Type", "application/json");
  if (method !== "GET" && method !== "HEAD") headers.set("X-CSRF-Token", csrfToken());

  const resp = await fetch(BASE + path, { ...init, headers, credentials: "same-origin" });
  if (resp.status === 204) return undefined as T;

  const text = await resp.text();
  let body: unknown = null;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = text;
    }
  }
  if (!resp.ok) {
    const msg =
      body && typeof body === "object" && "error" in body
        ? String((body as { error: unknown }).error)
        : `request failed (${resp.status})`;
    // Omitted by the server on every error that has nothing to branch on, so
    // absent is the common case and "" is the honest reading of it.
    const code =
      body && typeof body === "object" && "code" in body
        ? String((body as { code: unknown }).code)
        : "";
    throw new ApiError(resp.status, msg, code);
  }
  return body as T;
}

export const autoApi = {
  get: <T,>(p: string) => autoRequest<T>(p),
  post: <T,>(p: string, body?: unknown) =>
    autoRequest<T>(p, { method: "POST", body: body ? JSON.stringify(body) : undefined }),
  put: <T,>(p: string, body: unknown) =>
    autoRequest<T>(p, { method: "PUT", body: JSON.stringify(body) }),
  del: <T,>(p: string) => autoRequest<T>(p, { method: "DELETE" }),
};
