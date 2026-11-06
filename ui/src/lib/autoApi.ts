/* ===========================================================================
   The automation, clip, loudness and recording routes that have no named
   wrapper in lib/api.ts, called through its one transport (request).

   It lives apart from api.ts so the pages that only need these verbs do not
   pull in the whole `api` object; it holds no transport of its own, so it
   cannot drift from it. Errors are api.ts's ApiError, with `code`, the
   settings-conflict `storedVersion` and the reconcile-failure toast.
   =========================================================================== */

import { http, request, sourceQuery } from "./api";

export const autoApi = {
  get: <T,>(p: string) => http.get<T>(p),
  post: <T,>(p: string, body?: unknown) => http.post<T>(p, body),
  put: <T,>(p: string, body: unknown) => http.put<T>(p, body),
  del: <T,>(p: string) => http.del<T>(p),

  /* THE PROGRAMME-SCOPED ROUTES, and `sourceId` is REQUIRED on every one.

     handleListClips and handleLoudness call scopedEngine INSIDE the handler
     rather than carrying requireSource at the router, so a survey of the
     router's middleware does not find them; POST /clips, PUT /clips/buffer and
     PUT /loudness do carry it. All five are refused with 400 `source_required`
     on an install with two programmes, which left the clip list empty, the
     loudness analyser silent and capture/arm/switch buttons showing an
     instruction ("add ?source=<id>") nobody clicking can act on. Required so a
     caller that forgets does not compile. */
  listClips: <T,>(sourceId: number | null) =>
    request<T>("/clips" + sourceQuery(sourceId)),
  loudness: <T,>(sourceId: number | null) =>
    request<T>("/loudness" + sourceQuery(sourceId)),

  captureClip: <T,>(seconds: number, sourceId: number | null) =>
    request<T>("/clips" + sourceQuery(sourceId), {
      method: "POST",
      body: JSON.stringify({ seconds }),
    }),
  setClipBuffer: <T,>(
    body: { enabled: boolean; windowSeconds: number },
    sourceId: number | null,
  ) =>
    request<T>("/clips/buffer" + sourceQuery(sourceId), {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  setLoudnessMonitor: <T,>(enabled: boolean, sourceId: number | null) =>
    request<T>("/loudness" + sourceQuery(sourceId), {
      method: "PUT",
      body: JSON.stringify({ enabled }),
    }),
};
