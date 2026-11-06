/* ===========================================================================
   The five programme-scoped clip and loudness routes, called through
   lib/api.ts's one transport (request). Every other automation, clip and
   recording route goes through `http` in api.ts.

   Errors are api.ts's ApiError, with `code`, the settings-conflict
   `storedVersion` and the reconcile-failure toast.
   =========================================================================== */

import { request, sourceQuery } from "./api";

export const autoApi = {
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
