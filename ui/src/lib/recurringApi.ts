/* ===========================================================================
   The client for the recurring-event (#18) and planned-rollover (#19) routes.

   It is built on autoApi for JSON, with two exceptions:
   - the thumbnail is a raw image body. autoApi JSON-encodes every body and
     forces application/json, and the server checks image/png or image/jpeg
     against the bytes' magic numbers, so this module sends the real type.
   - the rollover routes are programme-scoped (requireSource), so every call
     that reaches a destination carries ?source= from the destination's own
     sourceId. A multi-programme install refuses the call without it.

   Errors are the same ApiError autoApi throws, so a caller branches on
   `status`, and the server's sentence stays the message.
   =========================================================================== */

import { ApiError } from "./api";
import { autoApi } from "./autoApi";
import { noteResponseStatus } from "./session";
import type { BroadcastTemplate, OccurrenceView, RolloverSettings, RolloverView } from "./types";

/** The 2 MB limit the server enforces on a thumbnail (scheduler.MaxThumbnailBytes). Checked here
 *  before the upload, so an oversized picture costs no round trip. */
export const MAX_THUMBNAIL_BYTES = 2 * 1024 * 1024;

/** The picture types the server accepts for a thumbnail. */
export const THUMBNAIL_TYPES = ["image/png", "image/jpeg"] as const;

function sourceParam(sourceId: number | null | undefined): string {
  return sourceId == null ? "" : `?source=${encodeURIComponent(String(sourceId))}`;
}

/** Loads a schedule's template, or null when the schedule has none. A 404 is the normal case for a
 *  schedule nobody has templated; every other failure is thrown. */
export async function getTemplate(scheduleId: number): Promise<BroadcastTemplate | null> {
  try {
    return await autoApi.get<BroadcastTemplate>(`/schedules/${scheduleId}/template`);
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) return null;
    throw err;
  }
}

export function saveTemplate(
  scheduleId: number,
  body: Pick<BroadcastTemplate, "titlePattern" | "description" | "privacy" | "category" | "lookahead">,
): Promise<BroadcastTemplate> {
  return autoApi.put<BroadcastTemplate>(`/schedules/${scheduleId}/template`, body);
}

export function deleteTemplate(scheduleId: number): Promise<void> {
  return autoApi.del<void>(`/schedules/${scheduleId}/template`);
}

/** Uploads a thumbnail as the raw body, with its real content type and the CSRF header. Throws
 *  before the request when the picture is the wrong type or too large. */
export async function uploadThumbnail(scheduleId: number, file: Blob, mime: string): Promise<void> {
  if (!(THUMBNAIL_TYPES as readonly string[]).includes(mime)) {
    throw new Error("a thumbnail must be a PNG or a JPEG");
  }
  if (file.size > MAX_THUMBNAIL_BYTES) {
    throw new Error("the thumbnail is larger than 2 MB");
  }
  const path = `/api/v1/schedules/${scheduleId}/template/thumbnail`;
  const resp = await fetch(path, {
    method: "PUT",
    body: file,
    headers: { "Content-Type": mime, "X-CSRF-Token": csrfToken() },
    credentials: "same-origin",
  });
  noteResponseStatus(path, resp.status);
  if (!resp.ok) {
    const text = await resp.text();
    let msg = `request failed (${resp.status})`;
    try {
      const body = JSON.parse(text) as { error?: unknown };
      if (typeof body.error === "string") msg = body.error;
    } catch {
      if (text) msg = text;
    }
    throw new ApiError(resp.status, msg, "");
  }
}

/** The calendar window, as RFC 3339 instants. The server refuses a window wider than 90 days. */
export async function listOccurrences(from: Date, to: Date): Promise<OccurrenceView[]> {
  const q = `?from=${encodeURIComponent(from.toISOString())}&to=${encodeURIComponent(to.toISOString())}`;
  const out = await autoApi.get<OccurrenceView[]>(`/recurring/occurrences${q}`);
  // The server always answers a list. Anything else is treated as an empty window, not a crash in render.
  return Array.isArray(out) ? out : [];
}

export function skipOccurrence(id: number): Promise<{ id: number; state: string }> {
  return autoApi.post<{ id: number; state: string }>(`/recurring/occurrences/${id}/skip`);
}

export function retitleOccurrence(id: number, title: string): Promise<{ id: number; title: string }> {
  return autoApi.post<{ id: number; title: string }>(`/recurring/occurrences/${id}/title`, { title });
}

export function getRollover(destinationId: number, sourceId: number | null | undefined): Promise<RolloverView> {
  return autoApi.get<RolloverView>(`/destinations/${destinationId}/rollover${sourceParam(sourceId)}`);
}

export function saveRollover(
  destinationId: number,
  sourceId: number | null | undefined,
  body: Pick<RolloverSettings, "enabled" | "marginMinutes" | "quietStart" | "quietEnd" | "quietZone" | "obsScene" | "chatNotice">,
): Promise<RolloverView> {
  return autoApi.put<RolloverView>(`/destinations/${destinationId}/rollover${sourceParam(sourceId)}`, body);
}

/** Runs a rollover now. The call is synchronous: a 200 means the rollover finished, a 502 means the
 *  platform answered and refused, and a 409 means the destination was not in a state to roll over. */
export function runRollover(
  destinationId: number,
  sourceId: number | null | undefined,
): Promise<{ status: string }> {
  return autoApi.post<{ status: string }>(`/destinations/${destinationId}/rollover/run${sourceParam(sourceId)}`);
}

/** The CSRF token from the cookie, as autoApi reads it. Kept as a copy: autoApi does not export it. */
function csrfToken(): string {
  const match = document.cookie.match(/(?:^|;\s*)polyemesis_csrf=([^;]+)/);
  return match ? decodeURIComponent(match[1]) : "";
}
