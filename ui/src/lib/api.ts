import type {
  AccountStats,
  AcmePreflight,
  ApiToken,
  TokenScope,
  AutomodMatrixView,
  AutomodModelStats,
  BitrateSample,
  BroadcastEnd,
  BulkDestReport,
  ChatMessage,
  ChatModerationResult,
  ChatOverview,
  ChatPlatform,
  ChatSearchResult,
  ChatSendResponse,
  ChatSettings,
  ChatUserCard,
  CredentialCheck,
  Destination,
  DestinationId,
  DeviceAuth,
  DevicePoll,
  DiskUsage,
  DryRunResult,
  EncoderList,
  ExpertArgs,
  ExpertResponse,
  FontInfo,
  JobKindInfo,
  JobsOverview,
  JobState,
  JobStats,
  JobView,
  Levels,
  LibraryRecording,
  LibrarySession,
  LibraryView,
  LogLine,
  MediaFile,
  Metadata,
  PlatformAccount,
  PlatformCreds,
  PlayoutAdminView,
  PlayoutProtection,
  PlayoutPublicView,
  PlayoutUrls,
  PlaylistStatus,
  PostProdSettings,
  Preset,
  PresetOpts,
  ProcessInfo,
  Recording,
  Rendition,
  RenditionBounds,
  RenditionDeleted,
  RenditionPreset,
  PlatformPresetInfo,
  ServiceInfo,
  RenditionView,
  RoutingProfile,
  RoutingResult,
  SearchParams,
  SearchResults,
  Settings,
  SetupGuide,
  Source,
  SourceId,
  SourceInfo,
  SourceView,
  Status,
  StreamHealthView,
  DebugState,
  SystemInfo,
  SystemStats,
  TlsStatus,
  TourState,
  TrackAnnotation,
  TranscriptTrack,
  TranscriptView,
  Hook,
  HookMeta,
  HookDelivery,
  HookTestResult,
  HookCreated,
  HookTrigger,
  VersionInfo,
  UpgradePlan,
  UpgradeResult,
} from "./types";

const BASE = "/api/v1";

/** Thrown for any non-2xx response, carrying the server's message. */
export class ApiError extends Error {
  // Written out longhand rather than as a parameter property: the build sets
  // erasableSyntaxOnly, which forbids TS syntax that emits runtime code.
  readonly status: number;
  /** The server's machine-readable reason, `""` when it sent none.
   *
   *  It exists so that a caller distinguishing one refusal from another does
   *  not have to match on `message`. `"no_source"` is the first: a 503 saying
   *  this install has no programme yet is an empty state, and every other 503
   *  is a fault -- telling them apart by reading the English would break on a
   *  reword and could never work once that sentence is translated. */
  readonly code: string;

  constructor(status: number, message: string, code = "") {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

/** The code the server sends when this install has no source yet.
 *
 *  Written once, here, next to the field it reads. Every screen that draws an
 *  empty state compares against it, and a string literal repeated across four
 *  pages is four chances to typo it into a comparison that is simply always
 *  false -- which fails by drawing the red toast, i.e. by looking exactly like
 *  the bug it was supposed to fix. lib/no-source-code.test.ts reads the Go
 *  constant and asserts this equals it. */
export const NO_SOURCE = "no_source";

/** Whether a rejected request failed because the install has no programme yet.
 *
 *  This is an EMPTY STATE, not a fault: nothing is broken, and the only thing
 *  missing is a source only the operator can create. A caller that treats it
 *  like any other error tells a first-time operator that their brand-new
 *  install is failing. */
export function isNoSource(e: unknown): boolean {
  return e instanceof ApiError && e.code === NO_SOURCE;
}

/** Read the double-submit CSRF token the server set as a readable cookie. */
function csrfToken(): string {
  const match = document.cookie.match(/(?:^|;\s*)polyemesis_csrf=([^;]+)/);
  return match ? decodeURIComponent(match[1]) : "";
}

async function request<T>(
  path: string,
  init: RequestInit = {},
): Promise<T> {
  const method = (init.method ?? "GET").toUpperCase();
  const headers = new Headers(init.headers);
  if (init.body) headers.set("Content-Type", "application/json");
  if (method !== "GET" && method !== "HEAD") {
    headers.set("X-CSRF-Token", csrfToken());
  }

  const resp = await fetch(BASE + path, {
    ...init,
    headers,
    credentials: "same-origin",
  });

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
        : typeof body === "string" && body
          ? body
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

const get = <T,>(p: string) => request<T>(p);
const post = <T,>(p: string, body?: unknown) =>
  request<T>(p, { method: "POST", body: body ? JSON.stringify(body) : undefined });
const put = <T,>(p: string, body: unknown) =>
  request<T>(p, { method: "PUT", body: JSON.stringify(body) });
// PATCH is not PUT with a different name here: the one caller sends a partial
// body where an omitted field means "leave it alone", and sending that to a PUT
// endpoint would ask the server to treat absent as empty.
const patch = <T,>(p: string, body: unknown) =>
  request<T>(p, { method: "PATCH", body: JSON.stringify(body) });
const del = <T,>(p: string) => request<T>(p, { method: "DELETE" });

/** `?t=<token>` when a playback token is in play, empty otherwise. The public
 *  routes accept the token this way because an <img> src and a <video> src are
 *  the only handles a player page has on those requests. */
function tokenQuery(token?: string): string {
  return token ? `?t=${encodeURIComponent(token)}` : "";
}

/** `?source=<id>` — WHICH PROGRAMME a request is for.
 *
 *  Issue #497: a handful of routes resolved their engine through the server's
 *  DEFAULT one, which is the right answer only on an install with a single
 *  source. Editing a track label while looking at programme 2 therefore
 *  rewrote programme 1's ingest and restarted its live destinations, with no
 *  click and no confirmation — a 500 ms debounce fired it.
 *
 *  The server now refuses these routes without an id whenever the install runs
 *  more than one programme, so an omitted argument here fails loudly on the
 *  installs where it would have been wrong and changes nothing on the ones
 *  where it cannot be. Optional in the type for exactly that reason: a
 *  destination row written before sources existed carries no `sourceId`, and
 *  guessing one on its behalf is the bug this parameter exists to end. */
function sourceQuery(sourceId?: number | null): string {
  return sourceId == null ? "" : `?source=${encodeURIComponent(String(sourceId))}`;
}

export interface DestinationWithRouting {
  destination: Destination;
  routing?: RoutingResult;
  routingError?: string;
}

/** Upload one file, reporting progress.
 *
 *  XMLHttpRequest rather than fetch, and not for nostalgia: fetch cannot report
 *  UPLOAD progress at all. Streams-based request bodies would allow it but are
 *  not available in Safari, and a multi-gigabyte upload with no progress bar is
 *  indistinguishable from a hung one.
 *
 *  Content-Type is deliberately NOT set. The browser must generate it, because
 *  only it knows the multipart boundary it just chose; setting it by hand
 *  produces a body the server cannot parse. */
export function uploadMedia(
  file: File,
  onProgress?: (fraction: number) => void,
  signal?: AbortSignal,
): Promise<MediaFile> {
  return new Promise<MediaFile>((resolve, reject) => {
    const form = new FormData();
    form.append("file", file, file.name);

    const xhr = new XMLHttpRequest();
    xhr.open("POST", BASE + "/media");
    xhr.withCredentials = true;
    xhr.setRequestHeader("X-CSRF-Token", csrfToken());

    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && onProgress) onProgress(e.loaded / e.total);
    };
    xhr.onload = () => {
      let body: unknown = null;
      try {
        body = JSON.parse(xhr.responseText);
      } catch {
        body = xhr.responseText;
      }
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(body as MediaFile);
        return;
      }
      const msg =
        body && typeof body === "object" && "error" in body
          ? String((body as { error: unknown }).error)
          : `upload failed (${xhr.status})`;
      reject(new ApiError(xhr.status, msg));
    };
    // A network failure and an abort are different things to a user: one is
    // "try again", the other is "you did that on purpose".
    xhr.onerror = () => reject(new ApiError(0, "the upload could not reach the server"));
    xhr.onabort = () => reject(new ApiError(0, "upload cancelled"));

    signal?.addEventListener("abort", () => xhr.abort(), { once: true });
    xhr.send(form);
  });
}

export const api = {
  // --- automod ---
  automodMatrix: () => get<AutomodMatrixView>("/automod/matrix"),
  automodStats: () => get<AutomodModelStats>("/automod/stats"),
  /** Sets or clears the model API key. Empty clears it. The key is never
   *  returned by any endpoint, so the UI can only ever report that one is set. */
  setAutomodKey: (key: string) =>
    put<{ hasApiKey: boolean }>("/settings/automod-key", { key }),

  // --- media uploads ---
  media: () => get<MediaFile[]>("/media"),
  deleteMedia: (name: string) => del<void>(`/media/${encodeURIComponent(name)}`),
  uploadMedia,
  /** Queues a re-inspection of a file already on the server (#202).
   *
   *  It returns as soon as the job is QUEUED, not when the file has been read:
   *  the inspection is an FFprobe against something that may be several
   *  gigabytes, on a box that is also encoding a broadcast, so it runs in the
   *  job queue under the resource policy. `created` is false when an identical
   *  re-check was already queued or running and this one folded into it.
   *
   *  The listing does not change on the way back. Callers refresh when the job
   *  finishes, not when this resolves. */
  verifyMedia: (name: string) =>
    post<{ job: JobView; created: boolean }>(
      `/media/${encodeURIComponent(name)}/verify`,
    ),

  // --- version ---
  /** The cached answer. Never triggers a network call to GitHub on its own. */
  version: () => get<VersionInfo>("/version"),
  /** Asks the server to consult the release feed. Rate-limited server-side by a
   *  6h TTL, so calling this on a click is safe. */
  checkUpdate: () => post<VersionInfo>("/version/check"),

  // --- upgrade ---
  /** What could be done about the available release, on this box. Ask on an
   *  operator's action, never on page load: building the answer creates a file
   *  in the install directory to find out whether it can be written to. */
  upgradePlan: () => get<UpgradePlan>("/upgrade/plan"),
  /** Download, verify and swap the binary. Does NOT restart anything.
   *
   *  `version` confirms the release the operator was looking at; it does not
   *  choose one. The server installs whatever its last check found and refuses
   *  if the two disagree, so a page left open overnight cannot install a
   *  release nobody read the notes for.
   *
   *  `force` overrides the on-air refusal. Only ever send it from an explicit
   *  confirmation that named what is live. */
  upgradeStage: (version: string, force = false) =>
    post<UpgradeResult>("/upgrade/stage", { version, force }),
  /** Put the previous binary back. Also does not restart anything. */
  upgradeRollback: (force = false) => post<UpgradeResult>("/upgrade/rollback", { force }),

  // --- setup & auth ---
  /** `sources` is how many programmes exist, and it is here rather than on a
   *  signed-in route because this is the only status a browser can read before
   *  it has an account. It is optional because the server omits it when the
   *  count cannot be read — absent means "unknown", which is not the same
   *  answer as 0 and must not be rendered as one. */
  setupStatus: () =>
    get<{ needsSetup: boolean; minPasswordChars: number; sources?: number }>("/setup"),
  setup: (username: string, password: string) =>
    post<{ username: string }>("/setup", { username, password }),
  login: (username: string, password: string) =>
    post<{ username: string }>("/auth/login", { username, password }),
  logout: () => post<{ status: string }>("/auth/logout"),
  me: () => get<{ username: string }>("/auth/me"),
  changePassword: (current: string, next: string) =>
    post<{ status: string }>("/auth/password", { current, new: next }),

  // --- api tokens ---
  listTokens: () => get<ApiToken[]>("/auth/tokens"),
  /**
   * `plaintext` is the only time the secret exists — nothing stores it, so a
   * caller that drops it has to mint a new token.
   */
  createToken: (name: string, scope: TokenScope) =>
    post<{ token: ApiToken; plaintext: string }>("/auth/tokens", { name, scope }),
  revokeToken: (id: number) => del<{ status: string }>(`/auth/tokens/${id}`),

  // --- onboarding tour ---
  /** Whether the tour should still be offered. */
  tourState: () => get<TourState>("/tour"),
  /**
   * Records that the tour was finished or dismissed — the two write the same
   * thing, because the offer is one-time either way and Settings carries the
   * replay control for anyone who changes their mind.
   *
   * Idempotent on the server: the first completion wins, so calling it twice
   * (finishing the last step also destroys the popover, which is the dismiss
   * path) cannot move the timestamp or un-complete it.
   */
  completeTour: () => post<TourState>("/tour/complete"),

  // --- system & telemetry ---
  system: (sourceId?: number | null) => get<SystemInfo>("/system" + sourceQuery(sourceId)),
  /* REQUIRED, not optional, and that is the whole point.
   *
   * These four describe ONE programme. The server refuses them with
   * `source_required` when an install has two or more and the request names
   * none -- because answering the default is what let five separate bugs report
   * programme 1's figures on somebody else's screen.
   *
   * Optional here would move that refusal from compile time to a 400 at 4pm on
   * a Friday. Required means a caller that has not decided which programme it
   * is asking about does not build, and TypeScript names every site. `system`
   * stays optional: most of what it returns describes the machine, and the
   * setup wizard reads it before any source exists. */
  status: (sourceId: number | null) => get<Status>("/status" + sourceQuery(sourceId)),
  source: (sourceId: number | null) => get<SourceInfo>("/source" + sourceQuery(sourceId)),
  levels: (sourceId: number | null) =>
    get<{ levels: Levels; at: string }>("/levels" + sourceQuery(sourceId)),
  stats: () =>
    get<{ system: SystemStats; bitrate: BitrateSample[] | null }>("/stats"),

  // --- settings ---
  getSettings: () => get<Settings>("/settings"),
  /** Saves the settings and returns THE SETTINGS, not the whole response.
   *
   *  PUT /settings answers with api.settingsResponse, which embeds db.Settings
   *  and adds a `reload` report describing what saving just did. This function
   *  used to be typed `put<Settings>` and hand that whole object back, so
   *  SettingsPage stored `reload` as part of its settings state and the NEXT
   *  save PUT it back into a decoder with DisallowUnknownFields -- 400,
   *  `unknown field "reload"`. A second save from one page load always failed.
   *
   *  tsc could never see it: the declared type said `Settings` while the wire
   *  carried more, so the assertion was simply untrue and nothing checks that.
   *  Stripping it HERE makes the declaration true, which is the only place it
   *  can be made true -- every caller downstream is entitled to believe it.
   *
   *  `reload` is discarded rather than surfaced because nothing reads it today.
   *  A caller that wants it should take it from a response type that says so. */
  putSettings: async (s: Settings): Promise<Settings> => {
    const { reload: _reload, ...saved } = await put<Settings & { reload?: unknown }>(
      "/settings",
      s,
    );
    return saved as Settings;
  },
  /** The MQTT broker password, on its own route.
   *
   *  Not a field on the settings blob: that blob travels outward on every
   *  settings read, and a write-only field inside a read-write payload is a
   *  trap -- a client that PUT back what it GOT would blank the password every
   *  time. An empty string CLEARS the stored password. */
  putMqttPassword: (password: string) =>
    put<{ hasPassword: boolean }>("/settings/mqtt-password", { password }),
  /** Per-item readiness for the configured playlist -- whether the upload
   *  behind each entry still exists and has what the selector needs to play
   *  it. Separate from getSettings() because settings only carries what the
   *  operator TYPED; this reports what the server can actually DO with it. */
  playlistStatus: () => get<PlaylistStatus>("/failover/playlist"),

  // --- transport security ---
  /** Read-only: TLS lives in config.yaml because it has to be right before the
   *  server starts listening. This only reports what that produced. */
  tlsStatus: () => get<TlsStatus>("/tls"),
  /** A full-page navigation so the browser runs its own download UI, and
   *  sessionless on the server so a user blocked by an untrusted certificate
   *  can still fetch the CA that unblocks them. 404s outside selfsigned mode. */
  caDownloadUrl: () => `${BASE}/tls/ca`,
  /** What Let's Encrypt would need from this host, before the restart that
   *  would otherwise be the way to find out. The hostname is sent rather than
   *  inferred from the request: an operator on `tls.mode: off` has none
   *  configured, and the browser already knows which name was typed. */
  acmePreflight: (hostname: string) =>
    get<AcmePreflight>(`/tls/acme-preflight?hostname=${encodeURIComponent(hostname)}`),

  // --- destinations ---
  listDestinations: () => get<DestinationWithRouting[]>("/destinations"),
  getDestination: (id: DestinationId) =>
    get<DestinationWithRouting>(`/destinations/${id}`),
  /**
   * `warnings` names any platform-specific settings the server dropped because
   * this destination's platform cannot send them — a COPPA declaration left
   * behind by a platform change, say. Present only when something was dropped.
   */
  createDestination: (d: Partial<Destination>) =>
    post<{ destination: Destination; warnings?: string[] }>("/destinations", d),
  updateDestination: (id: DestinationId, d: Partial<Destination>) =>
    put<DestinationWithRouting & { warnings?: string[] }>(`/destinations/${id}`, d),
  deleteDestination: (id: DestinationId) => del<{ status: string }>(`/destinations/${id}`),
  /** Display order only — the server does not restart anything for this. */
  reorderDestinations: (ids: DestinationId[]) =>
    put<{ ids: DestinationId[] }>("/destinations/order", { ids }),
  startDestination: (id: DestinationId) =>
    post<{ enabled: boolean }>(`/destinations/${id}/start`),
  stopDestination: (id: DestinationId) =>
    post<{ enabled: boolean }>(`/destinations/${id}/stop`),
  restartDestination: (id: DestinationId) =>
    post<{ status: string }>(`/destinations/${id}/restart`),

  /* --- every destination at once ----------------------------------------
   *
   *  These are startDestination/stopDestination run over every row on the
   *  server, not a new capability: same handler code, same reconcile, same
   *  read-back. There is no id list because there is no selection — the
   *  routes act on the whole install.
   *
   *  SLOW ON PURPOSE. Starts are paced server-side, so this resolves after
   *  the last destination has been dealt with rather than on acknowledgement.
   *  A caller that aborts leaves the untouched tail reported as `skipped`.
   *
   *  STOPPING ENDS EVERY YOUTUBE BROADCAST ON THE INSTALL, PERMANENTLY: stop
   *  and disable are one thing on the server, and the broadcast lifecycle
   *  coordinator ends the broadcast of a disabled destination. Starting again
   *  puts video back on the wire; it does not bring the broadcasts back. See
   *  internal/api/destinations_bulk.go. */
  startAllDestinations: () => post<BulkDestReport>("/destinations/start-all"),
  /** Stop every destination. Carries `confirm` because the server refuses this
   *  route without it: it ends every live broadcast on the install, and a
   *  broadcast that has ended cannot be resumed. The dialog in front of this
   *  call is what the flag attests to -- the server cannot see that dialog, so
   *  a caller that never showed one is exactly who the refusal is for. */
  stopAllDestinations: () =>
    post<BulkDestReport>("/destinations/stop-all", { confirm: true }),
  refreshStreamKey: (id: DestinationId) =>
    post<{ destination: Destination }>(`/destinations/${id}/refresh-key`),

  /* --- Facebook broadcast lifecycle -------------------------------------
   *
   *  NEITHER ROUTE BELOW EXISTS ON THE SERVER YET, AND THAT IS STATED HERE
   *  RATHER THAN DISCOVERED AT RUNTIME. internal/oauth/facebook.go has both
   *  capabilities — EndBroadcast at :870 and StreamHealth at :1020 — and
   *  internal/api exposes neither: grepping the package for `EndBroadcast`,
   *  `StreamHealth` or `end_live_video` returns nothing. The UI was the half
   *  that was missing, so the UI is what was built; the handlers are a
   *  separate change and were deliberately NOT written here, because adding a
   *  route that reaches a live platform is not a side effect of building a
   *  card.
   *
   *  So these two functions are the CONTRACT REQUEST, written down in the one
   *  place a handler author will look. The shapes are the Go types as they
   *  already stand, so nothing has to be redesigned to satisfy them:
   *
   *    POST /api/v1/destinations/{id}/facebook/end-broadcast  -> BroadcastEnd
   *      Resolves the destination's Facebook account and live video id, calls
   *      EndBroadcast, and answers its BroadcastEnd verbatim. A refused POST
   *      is a 4xx/5xx with the provider's message, per EndBroadcast's own
   *      note that a failed end is an error rather than a skip — it leaves a
   *      broadcast ON AIR that the operator believes is over.
   *
   *    GET  /api/v1/destinations/{id}/facebook/stream-health -> StreamHealthView
   *      { supported, streams }. supported:false with 200 for a destination on
   *      a platform that publishes no stream health, exactly as the viewer
   *      stats endpoint already answers, so "we cannot ask" stays separable
   *      from "the account is gone". An EMPTY streams list is a 200 and not an
   *      error: a scheduled broadcast has no ingest yet and a live one inside
   *      Facebook's four-second timeout reports nothing.
   *
   *  Until those land, the panel below treats a 404 or 501 as "this server
   *  does not expose it" — a stated absence in muted text, not a fault, and it
   *  stops polling rather than knocking on a missing door every two seconds.
   */
  /** Ends the destination's Facebook live video. See the block above: the
   *  route is not implemented server-side yet. */
  endFacebookBroadcast: (id: DestinationId) =>
    post<BroadcastEnd>(`/destinations/${id}/facebook/end-broadcast`),
  /** Reads Facebook's view of the encoder feed. Callers must not poll faster
   *  than FACEBOOK_STREAM_HEALTH_INTERVAL_MS — that is Facebook's published
   *  floor, quoted beside the constant in types.ts. See the block above: the
   *  route is not implemented server-side yet. */
  facebookStreamHealth: (id: DestinationId) =>
    get<StreamHealthView>(`/destinations/${id}/facebook/stream-health`),

  // --- debug mode ---
  debugState: () => get<DebugState>("/debug"),
  setDebug: (body: { recording?: boolean; reset?: boolean }) =>
    put<DebugState>("/debug", body),
  /** Downloads the debug bundle.
   *
   *  NOT `<a href download>` like the clip and recording downloads, and the
   *  difference is deliberate rather than incidental: the export is a POST so
   *  that it is CSRF-covered and so that a browser prefetching a link cannot
   *  put an entry in the audit trail. A POST cannot be an anchor, so the body
   *  is fetched and handed to the browser as a blob.
   *
   *  Returns the text and the filename the server chose, so the caller does the
   *  saving — this module does not touch the DOM. */
  exportDebug: async (): Promise<{ text: string; filename: string }> => {
    const resp = await fetch(`${BASE}/debug/export`, {
      method: "POST",
      // The same double-submit token every other non-GET carries. Built here
      // rather than through request() because that helper parses the body as
      // JSON, and this one needs the raw text and the Content-Disposition.
      headers: { "X-CSRF-Token": csrfToken() },
      credentials: "same-origin",
    });
    const text = await resp.text();
    if (!resp.ok) {
      let msg = `export failed (${resp.status})`;
      try {
        const parsed = JSON.parse(text) as { error?: string };
        if (parsed.error) msg = parsed.error;
      } catch {
        /* a non-JSON body; the status is all there is to report */
      }
      throw new ApiError(resp.status, msg);
    }
    // The server names the file with a timestamp so two bundles in one support
    // thread can be told apart. Fall back only if the header is missing.
    const cd = resp.headers.get("Content-Disposition") ?? "";
    const m = /filename="([^"]+)"/.exec(cd);
    return { text, filename: m?.[1] ?? "polyemesis-debug.json" };
  },

  // --- sources ---
  // A source is one ingested programme. Everything else -- destinations,
  // renditions, recordings -- belongs to exactly one of them. Unscoped calls
  // act on the default source, which is what keeps every other page working
  // without knowing sources exist.
  listSources: () => get<SourceView[]>("/sources"),
  getSource: (id: SourceId) => get<SourceView>(`/sources/${id}`),
  createSource: (s: Partial<Source>) => post<SourceView>("/sources", s),
  updateSource: (id: SourceId, s: Partial<Source>) =>
    put<SourceView>(`/sources/${id}`, s),
  deleteSource: (id: SourceId) => del<void>(`/sources/${id}`),
  rotateSourceToken: (id: SourceId) =>
    post<SourceView>(`/sources/${id}/token`),

  // --- renditions ---
  // A rendition is one shared video encode several destinations can select, so
  // N destinations wanting 1080p60 cost one encode rather than N. A destination
  // with no rendition is passthrough, which is the default and costs nothing.
  /** The server's platform catalogue, including researched encoder guidance.
   *  Fetched rather than mirrored: the numbers carry a source and a date, and
   *  a second copy of them in the UI would drift silently. */
  platformPresets: () =>
    get<{ presets: PlatformPresetInfo[]; disclaimer: string }>("/platforms/presets"),
  /** The platform registry: ingest servers, encoder ceilings and codecs.
   *
   *  Seeded from OBS Studio's rtmp-services data, and `provenance` says so —
   *  these are the platforms' published figures, copied, not ours. Shown to
   *  the operator with that attribution rather than as house numbers. */
  listServices: () =>
    get<{ provenance: string; services: ServiceInfo[] }>("/services"),
  listRenditions: () => get<RenditionView[]>("/renditions"),
  getRendition: (id: number) => get<RenditionView>(`/renditions/${id}`),
  createRendition: (r: Partial<Rendition>) =>
    post<{ rendition: Rendition }>("/renditions", r),
  updateRendition: (id: number, r: Partial<Rendition>) =>
    put<{ rendition: Rendition }>(`/renditions/${id}`, r),
  /** Succeeds even while destinations use it — they drop to passthrough, and
   *  the response says how many did. */
  deleteRendition: (id: number) => del<RenditionDeleted>(`/renditions/${id}`),
  restartRendition: (id: number) =>
    post<{ status: string }>(`/renditions/${id}/restart`),
  renditionPresets: () =>
    get<{
      presets: RenditionPreset[];
      disclaimer: string;
      bounds: RenditionBounds;
    }>("/renditions/presets"),
  /** The fonts on disk for text overlays, plus whether this FFmpeg can draw
   *  text at all.
   *
   *  A listing rather than a compiled-in list: <data>/fonts holds the built-ins
   *  AND anything the operator dropped in, and a hardcoded array would show two
   *  entries forever. `textSupported` is false on a build without libfreetype,
   *  where drawtext does not exist and the settings would never render. */
  fonts: () =>
    get<{
      fonts: FontInfo[];
      defaultFont: string;
      dir: string;
      textSupported: boolean;
    }>("/fonts"),
  /** Every known encoder, each with whether it actually encoded a frame on this
   *  machine and why not when it did not. The list is deliberately complete:
   *  "h264_nvenc — no NVENC capable device found" tells the user their container
   *  is missing --gpus, where a silently shorter list teaches them nothing. */
  listEncoders: () => get<EncoderList>("/encoders"),
  /** Re-run the GPU scan and every test encode before answering.
   *
   *  A GET because it is a read of the machine's current state — the same
   *  answer, not from the cache. Hardware moves after launch: a driver package
   *  upgrades, a card is passed into the container, a laptop comes back from
   *  suspend with a render node that now opens. Takes a few seconds, and is
   *  single-flighted server-side so a second click costs nothing. */
  redetectEncoders: () => get<EncoderList>("/encoders?redetect=1"),

  // --- routing ---
  /** Compiles against the engine's live source layout, which is what makes the
   *  filter string under the editor honest: it comes from the same Go code
   *  that will run, not from a TypeScript reimplementation. */
  compileRouting: (profile: RoutingProfile, sourceId?: number | null) =>
    post<{ routing: RoutingResult; profile: RoutingProfile }>(
      "/routing/compile" + sourceQuery(sourceId),
      { profile },
    ),
  listPresets: () =>
    get<{ presets: Preset[]; defaults: PresetOpts }>("/routing/presets"),
  applyPreset: (id: string, opts: PresetOpts, sourceId?: number | null) =>
    post<{ profile: RoutingProfile; routing: RoutingResult }>(
      `/routing/presets/${encodeURIComponent(id)}` + sourceQuery(sourceId),
      opts,
    ),

  /** Describe the ingest's tracks: role, label, language, denoise.
   *
   *  Annotations belong to the SOURCE, not to a destination — "track 2 is the
   *  licensed music" is one fact, and every destination's role policy resolves
   *  against it. They are read back from `GET /source`.
   *
   *  A server that predates the feature has no route here and answers 404. The
   *  routing editor treats that as "not stored yet" and keeps editing locally
   *  rather than refusing the whole page: a missing endpoint is not a reason to
   *  take the mixer away.
   *
   *  `sourceId` is WHICH programme's tracks these describe, and it is not
   *  optional in spirit — see sourceQuery and #497. This write restarts the
   *  live destinations whose graph it changes, so an unscoped one restarted a
   *  programme the operator was not looking at. */
  putAnnotations: (annotations: TrackAnnotation[], sourceId?: number | null) =>
    put<{ annotations: TrackAnnotation[] | null }>(
      "/source/annotations" + sourceQuery(sourceId),
      { annotations },
    ),

  // --- recordings ---
  listRecordings: () => get<Recording[]>("/recordings"),
  recordingUsage: () => get<DiskUsage>("/recordings/usage"),
  deleteRecording: (id: number) => del<{ status: string }>(`/recordings/${id}`),
  downloadUrl: (id: number) => `${BASE}/recordings/${id}/download`,

  // --- playout (the public origin) ---
  // Distinct from the dashboard preview: that is an admin-only 360p re-encode,
  // this is the viewer-facing HLS/DASH origin. Variant configuration lives in
  // Settings.playout and is saved through putSettings; everything here is the
  // publishing half — who may watch, and what the player page says.
  playout: () => get<PlayoutAdminView>("/playout"),
  /** Partial: an omitted field is left alone rather than blanked. */
  updatePlayoutPublish: (body: {
    protection?: PlayoutProtection;
    title?: string;
    description?: string;
  }) => put<{ protection: PlayoutProtection; title: string; description: string }>(
    "/playout/publish",
    body,
  ),
  /** Mints a new playback secret, which is the revocation mechanism: every
   *  link already handed out stops working. */
  rotatePlayoutToken: () =>
    post<{ token: string; urls: PlayoutUrls }>("/playout/token"),
  resetPlayoutAnalytics: () => post<unknown>("/playout/analytics/reset"),

  /** The public player's read. Unauthenticated, so it is called with a plain
   *  fetch rather than through `request`, which assumes a session. `token` is
   *  the shared playback secret from a /watch/:token link. */
  playoutPublic: async (token?: string): Promise<PlayoutPublicView> => {
    const url = BASE + "/playout/public" + tokenQuery(token);
    // credentials are included so the cookie the server hands back on the
    // first authorised request carries the following ones.
    const resp = await fetch(url, { credentials: "same-origin" });
    if (!resp.ok) {
      throw new ApiError(resp.status, `request failed (${resp.status})`);
    }
    return (await resp.json()) as PlayoutPublicView;
  },
  /** Poster image URL. A plain <img> src, so the token rides in the query. */
  playoutPosterUrl: (token?: string) =>
    BASE + "/playout/poster.jpg" + tokenQuery(token),
  /** The HLS ladder. Relative, so it works under whatever host or proxy prefix
   *  the viewer reached the page through. */
  playoutMasterUrl: (token?: string) =>
    "/playout/master.m3u8" + tokenQuery(token),

  // --- monitoring ---
  /** Every process is called `ingest`, `recorder`, `preview` or `meters` in
   *  EVERY engine — the name says what the child does, not which programme it
   *  does it for — so without `sourceId` a multi-source install served
   *  programme 1's FFmpeg log for a question about programme 2 (#497). The
   *  server now refuses instead of answering for the wrong one. */
  /* REQUIRED, for the same reason status/source/levels are: handleListProcesses
     and handleProcessLogs both call scopedEngine, which refuses with 400
     source_required on any install with two programmes. Optional meant
     MonitoringPage simply never passed one and the whole page was dead there. */
  listProcesses: (sourceId: number | null) =>
    get<ProcessInfo[]>("/processes" + sourceQuery(sourceId)),
  processLogs: (name: string, sourceId: number | null) =>
    get<{ name: string; command: string; lines: LogLine[] | null }>(
      `/processes/${encodeURIComponent(name)}/logs` + sourceQuery(sourceId),
    ),

  // --- expert mode ---
  //
  // Preview and dry-run are POSTs because they carry a candidate edit in the
  // body, not because they change anything: neither writes.
  getExpert: (id: DestinationId) => get<ExpertResponse>(`/destinations/${id}/expert`),
  previewExpert: (id: DestinationId, args: ExpertArgs) =>
    post<ExpertResponse>(`/destinations/${id}/expert/preview`, args),
  dryRunExpert: (id: DestinationId, args: ExpertArgs) =>
    post<DryRunResult>(`/destinations/${id}/expert/dry-run`, args),
  putExpert: (id: DestinationId, args: ExpertArgs & { confirm: boolean }) =>
    put<ExpertResponse>(`/destinations/${id}/expert`, args),
  deleteExpert: (id: DestinationId) => del<ExpertResponse>(`/destinations/${id}/expert`),

  // --- background jobs ---
  //
  // Nothing here starts work directly. Every call either reads the queue or
  // changes the policy that decides when the queue may have the machine; the
  // stream always wins, and that is the architecture rather than a setting.
  //
  // Each of these answers 503 on a server with no queue wired, which the pages
  // treat as "this capability is absent" rather than as an error.
  jobsOverview: () => get<JobsOverview>("/jobs/overview"),
  listJobs: (opts: {
    states?: (JobState | "active")[];
    kinds?: string[];
    recordingId?: number;
    limit?: number;
  } = {}) => {
    const q = new URLSearchParams();
    if (opts.states?.length) q.set("state", opts.states.join(","));
    if (opts.kinds?.length) q.set("kind", opts.kinds.join(","));
    if (opts.recordingId) q.set("recordingId", String(opts.recordingId));
    if (opts.limit) q.set("limit", String(opts.limit));
    const qs = q.toString();
    return get<{ jobs: JobView[]; stats: JobStats; paused: boolean }>(
      "/jobs" + (qs ? `?${qs}` : ""),
    );
  },
  getJob: (id: number) => get<JobView>(`/jobs/${id}`),
  cancelJob: (id: number) => post<JobView>(`/jobs/${id}/cancel`),
  retryJob: (id: number) => post<JobView>(`/jobs/${id}/retry`),
  /** Releases ONE job from the governor's gates. Deliberately not per-kind:
   *  "run this one now" must not become "and every one from now on". */
  releaseJob: (id: number) =>
    post<{ released: boolean; governed: boolean }>(`/jobs/${id}/release`),
  deleteJob: (id: number) => del<{ status: string }>(`/jobs/${id}`),
  pauseJobs: () => post<{ paused: boolean }>("/jobs/pause"),
  resumeJobs: () => post<{ paused: boolean }>("/jobs/resume"),
  purgeJobs: (body: { days?: number; keep?: number }) =>
    post<{ purged: number }>("/jobs/purge", body),

  jobPolicy: () =>
    get<{ policy: PostProdSettings; kinds: JobKindInfo[]; modes: string[] }>(
      "/jobs/policy",
    ),
  /** `restartRequired` is true when concurrency changed: the queue fixes that
   *  when it is constructed, so the new value is saved but not yet live. */
  putJobPolicy: (policy: PostProdSettings) =>
    put<{
      policy: PostProdSettings;
      kinds: JobKindInfo[];
      restartRequired: boolean;
    }>("/jobs/policy", policy),

  // --- the media library ---
  library: () => get<LibraryView>("/library"),
  librarySession: (id: number) =>
    get<{ session: LibrarySession; recordings: LibraryRecording[] }>(
      `/library/sessions/${id}`,
    ),
  createLibrarySession: (m: Metadata & { recordings?: number[] }) =>
    post<LibrarySession>("/library/sessions", m),
  /** Omitting `recordings` leaves the membership alone, so renaming a session
   *  cannot silently empty it. */
  updateLibrarySession: (id: number, m: Metadata & { recordings?: number[] }) =>
    put<LibrarySession>(`/library/sessions/${id}`, m),
  /** Ungroups the segments; it never deletes media. */
  deleteLibrarySession: (id: number) =>
    del<{ status: string; note: string }>(`/library/sessions/${id}`),
  /** Re-runs the grouping backfill: additive, idempotent, and it never merges
   *  a grouping a human has already split. */
  regroupSessions: () =>
    post<{
      created: number;
      assigned: number;
      extended: number;
      groups: number;
      pruned: number;
    }>("/library/sessions/regroup"),

  libraryRecording: (id: number) =>
    get<{ recording: LibraryRecording; transcriptTracks: TranscriptTrack[] | null }>(
      `/library/recordings/${id}`,
    ),
  updateLibraryRecording: (id: number, m: Metadata) =>
    put<Metadata & { recordingId: number }>(`/library/recordings/${id}`, m),

  transcript: (id: number) =>
    get<TranscriptView>(`/library/recordings/${id}/transcript`),
  deleteTranscript: (id: number, track?: number) =>
    del<{ status: string }>(
      `/library/recordings/${id}/transcript` +
        (track === undefined ? "" : `?track=${track}`),
    ),
  /** The manual half of the free-diarization story: the tracks already
   *  separate the voices, this is where "track 2" becomes "Ana". */
  setTranscriptSpeaker: (id: number, track: number, speaker: string) =>
    put<{ track: number; speaker: string }>(
      `/library/recordings/${id}/speaker`,
      { track, speaker },
    ),

  /** The headline feature. A GET with query parameters, so a result is a place:
   *  it survives being bookmarked, shared and reloaded. */
  searchTranscripts: (p: SearchParams) => {
    const q = new URLSearchParams();
    q.set("q", p.q);
    if (p.prefix) q.set("prefix", "1");
    if (p.raw) q.set("raw", "1");
    if (p.recordingId) q.set("recordingId", String(p.recordingId));
    if (p.sessionId) q.set("sessionId", String(p.sessionId));
    if (p.track !== undefined) q.set("track", String(p.track));
    if (p.speaker) q.set("speaker", p.speaker);
    if (p.since) q.set("since", p.since);
    if (p.until) q.set("until", p.until);
    if (p.order) q.set("order", p.order);
    if (p.limit !== undefined) q.set("limit", String(p.limit));
    if (p.offset !== undefined) q.set("offset", String(p.offset));
    if (p.context !== undefined) q.set("context", String(p.context));
    return get<SearchResults>(`/library/search?${q.toString()}`);
  },

  /** Queue one piece of post-production about one recording. The response's
   *  `created` is false when the queue folded it into work already running,
   *  which is what stops a double click transcribing twice. */
  submitRecordingJob: (id: number, kind: string, body: Record<string, unknown> = {}) =>
    post<{ job: JobView; created: boolean }>(
      `/library/recordings/${id}/jobs/${encodeURIComponent(kind)}`,
      body,
    ),

  /** A derived file's URL. Used as a <video> or <img> src, so it has to be a
   *  plain authenticated GET — a media element attaches no headers of its own. */
  libraryMediaUrl: (id: number, file: string) =>
    `${BASE}/library/recordings/${id}/media/${encodeURIComponent(file)}`,

  // --- unified chat ---
  //
  // Live messages do NOT come through here. They arrive on the WebSocket as
  // `chat` and `chatState` events; these four are the scrollback a freshly
  // opened pane needs plus the three things a socket cannot do.
  //
  // `configured: false` is the answer on a server with no chat wired, rather
  // than a 503, because the stored scrollback is still worth showing and a
  // page that refuses to render teaches the operator nothing.
  chatOverview: (limit?: number) =>
    get<ChatOverview>("/chat" + (limit ? `?limit=${limit}` : "")),
  /** Older scrollback, out of the database rather than the live ring. */
  chatMessages: (opts: { platform?: ChatPlatform; limit?: number } = {}) => {
    const q = new URLSearchParams();
    if (opts.platform) q.set("platform", opts.platform);
    if (opts.limit) q.set("limit", String(opts.limit));
    const qs = q.toString();
    return get<{ messages: ChatMessage[]; stored: boolean }>(
      "/chat/messages" + (qs ? `?${qs}` : ""),
    );
  },
  /** Find a message again, by its text or by who said it.
   *
   *  Server-side and against the database, not the pane: the timeline holds one
   *  session's worth of messages and "where did that comment go" is exactly the
   *  question it cannot answer. Bounded by the chat retention setting, which is
   *  why the response carries a note saying so. */
  chatSearch: (opts: { q: string; platform?: ChatPlatform; limit?: number }) => {
    const p = new URLSearchParams({ q: opts.q });
    if (opts.platform) p.set("platform", opts.platform);
    if (opts.limit) p.set("limit", String(opts.limit));
    return get<ChatSearchResult>(`/chat/search?${p.toString()}`);
  },
  /** Fan-out. Answers 200 even when every platform failed: the per-platform
   *  verdicts are the answer, and a status code cannot say "Twitch took it and
   *  YouTube did not". */
  sendChat: (text: string) => post<ChatSendResponse>("/chat/send", { text }),
  /** Moderator delete, on the platform that issued the id. A platform that
   *  cannot do it answers with a sentence saying so and naming where it can be
   *  done instead — the button is never hidden on a guess. */
  deleteChatMessage: (m: { platform: ChatPlatform; account?: string; id: string }) => {
    const q = new URLSearchParams({ platform: m.platform, id: m.id });
    if (m.account) q.set("account", m.account);
    return del<{ status: string }>(`/chat/messages?${q.toString()}`);
  },

  /** Hide a message. scope "local" affects this server only and viewers still
   *  see it; scope "platform" hides it from viewers, and only Facebook can. */
  hideChatMessage: (m: {
    platform: ChatPlatform;
    account?: string;
    id: string;
    scope: "local" | "platform";
    hidden?: boolean;
  }) => {
    const q = new URLSearchParams({ platform: m.platform, id: m.id, scope: m.scope });
    if (m.account) q.set("account", m.account);
    if (m.hidden === false) q.set("hidden", "false");
    return post<ChatModerationResult>(`/chat/messages/hide?${q.toString()}`, {});
  },

  /** Ban or time a viewer out. Seconds, always — the server converts for the
   *  platforms that count in minutes. Omit seconds for a permanent ban. */
  banChatUser: (m: {
    platform: ChatPlatform;
    account?: string;
    userId: string;
    seconds?: number;
    reason?: string;
  }) => {
    const q = new URLSearchParams({ platform: m.platform, userId: m.userId });
    if (m.account) q.set("account", m.account);
    if (m.seconds && m.seconds > 0) q.set("seconds", String(m.seconds));
    if (m.reason) q.set("reason", m.reason);
    return post<ChatModerationResult>(`/chat/bans?${q.toString()}`, {});
  },

  unbanChatUser: (m: { platform: ChatPlatform; account?: string; userId: string }) => {
    const q = new URLSearchParams({ platform: m.platform, userId: m.userId });
    if (m.account) q.set("account", m.account);
    return del<ChatModerationResult>(`/chat/bans?${q.toString()}`);
  },

  /** One person's recent messages, from this server's own scrollback. No
   *  platform publishes a chat-history API; see ChatUserCard. */
  chatUser: (m: { platform: ChatPlatform; authorId: string; limit?: number }) => {
    const q = new URLSearchParams({ platform: m.platform, authorId: m.authorId });
    if (m.limit) q.set("limit", String(m.limit));
    return get<ChatUserCard>(`/chat/users?${q.toString()}`);
  },

  /** Channel-wide chat rules. Only Twitch publishes an API for these, and an
   *  omitted field means "leave it alone" rather than "turn it off". */
  updateChatSettings: (platform: ChatPlatform, account: string | undefined, s: ChatSettings) => {
    const q = new URLSearchParams({ platform });
    if (account) q.set("account", account);
    return patch<{ status: string }>(`/chat/settings?${q.toString()}`, s);
  },

  // --- platforms ---
  platformGuides: () => get<SetupGuide[]>("/platforms/guides"),
  listCreds: () => get<PlatformCreds[]>("/platforms/credentials"),
  putCreds: (platform: string, clientId: string, clientSecret: string) =>
    put<{ platform: string; hasSecret: boolean; check: CredentialCheck }>(
      `/platforms/credentials/${platform}`,
      { clientId, clientSecret },
    ),
  /** Re-runs the check against what is stored, so an operator who has just
   *  fixed something in the platform console can retest without pasting the
   *  secret again -- which most consoles show exactly once. */
  checkCreds: (platform: string) =>
    post<CredentialCheck>(`/platforms/credentials/${platform}/check`),
  /** Starts the device code flow: the way to connect an account from a box no
   *  platform can redirect back to.
   *
   *  A POST despite creating no row, because it makes an outbound call to the
   *  platform — the same reasoning that makes `checkCreds` one. */
  startDeviceAuth: (platform: string) =>
    post<DeviceAuth>(`/platforms/credentials/${platform}/device`),
  /** Redeems the device code ONCE.
   *
   *  It does not loop and it does not sleep: pacing is the caller's, because a
   *  request that slept for the code's lifetime could not be cancelled. Wait
   *  `devicePollDelayMs(res.retryInSeconds)` between calls — the platform
   *  rate-limits the operator's whole app, not this feature. */
  pollDeviceAuth: (platform: string, handle: string) =>
    post<DevicePoll>(`/platforms/credentials/${platform}/device/poll`, { handle }),
  deleteCreds: (platform: string) =>
    del<{ status: string }>(`/platforms/credentials/${platform}`),
  listAccounts: () => get<PlatformAccount[]>("/platforms/accounts"),
  deleteAccount: (id: number) =>
    del<{ status: string }>(`/platforms/accounts/${id}`),
  /** The live viewer count for one connected account.
   *
   *  200 in every non-fatal case, including the two that look like failures and
   *  are not: a platform polyemesis cannot ask answers `supported:false` with a
   *  reason, and an offline channel answers `live:false`. Neither is an error,
   *  so neither throws — only a real fault (the account is gone, the platform
   *  refused the token) reaches the catch. `viewerCount` is absent rather than
   *  zero when the platform declined to say; see AccountStats. */
  accountStats: (id: number) =>
    get<AccountStats>(`/platforms/accounts/${id}/stats`),
  /** A full-page navigation, not XHR: the platform's consent screen owns the tab. */
  connectUrl: (platform: string) => `${BASE}/oauth/${platform}/start`,

  hooks: {
    meta: () => get<HookMeta>("/hooks/meta"),
    list: () => get<Hook[]>("/hooks"),
    create: (body: Partial<Hook> & { url: string; secret?: string }) =>
      post<HookCreated>("/hooks", body),
    update: (id: number, body: Partial<Hook> & { secret?: string }) =>
      put<Hook>(`/hooks/${id}`, body),
    remove: (id: number) => del<{ status: string }>(`/hooks/${id}`),
    test: (id: number, trigger?: HookTrigger) =>
      post<HookTestResult>(
        `/hooks/${id}/test${trigger ? `?trigger=${trigger}` : ""}`,
        {},
      ),
    deliveries: (id: number) => get<HookDelivery[]>(`/hooks/${id}/deliveries`),
  },
};
