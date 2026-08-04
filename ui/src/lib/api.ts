import type {
  ApiToken,
  AutomodMatrixView,
  AutomodModelStats,
  BitrateSample,
  ChatMessage,
  ChatModerationResult,
  ChatOverview,
  ChatPlatform,
  ChatSendResponse,
  ChatSettings,
  ChatUserCard,
  CredentialCheck,
  Destination,
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
  RenditionView,
  RoutingProfile,
  RoutingResult,
  SearchParams,
  SearchResults,
  Settings,
  SetupGuide,
  Source,
  SourceInfo,
  SourceView,
  Status,
  SystemInfo,
  SystemStats,
  TlsStatus,
  TrackAnnotation,
  TranscriptTrack,
  TranscriptView,
  Hook,
  HookMeta,
  HookDelivery,
  HookTestResult,
  HookCreated,
  HookTrigger
} from "./types";

const BASE = "/api/v1";

/** Thrown for any non-2xx response, carrying the server's message. */
export class ApiError extends Error {
  // Written out longhand rather than as a parameter property: the build sets
  // erasableSyntaxOnly, which forbids TS syntax that emits runtime code.
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
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
    throw new ApiError(resp.status, msg);
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

  // --- setup & auth ---
  setupStatus: () =>
    get<{ needsSetup: boolean; minPasswordChars: number }>("/setup"),
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
  createToken: (name: string) =>
    post<{ token: ApiToken; plaintext: string }>("/auth/tokens", { name }),
  revokeToken: (id: number) => del<{ status: string }>(`/auth/tokens/${id}`),

  // --- system & telemetry ---
  system: () => get<SystemInfo>("/system"),
  status: () => get<Status>("/status"),
  source: () => get<SourceInfo>("/source"),
  levels: () => get<{ levels: Levels; at: string }>("/levels"),
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

  // --- destinations ---
  listDestinations: () => get<DestinationWithRouting[]>("/destinations"),
  getDestination: (id: number) =>
    get<DestinationWithRouting>(`/destinations/${id}`),
  /**
   * `warnings` names any platform-specific settings the server dropped because
   * this destination's platform cannot send them — a COPPA declaration left
   * behind by a platform change, say. Present only when something was dropped.
   */
  createDestination: (d: Partial<Destination>) =>
    post<{ destination: Destination; warnings?: string[] }>("/destinations", d),
  updateDestination: (id: number, d: Partial<Destination>) =>
    put<DestinationWithRouting & { warnings?: string[] }>(`/destinations/${id}`, d),
  deleteDestination: (id: number) => del<{ status: string }>(`/destinations/${id}`),
  /** Display order only — the server does not restart anything for this. */
  reorderDestinations: (ids: number[]) =>
    put<{ ids: number[] }>("/destinations/order", { ids }),
  startDestination: (id: number) =>
    post<{ enabled: boolean }>(`/destinations/${id}/start`),
  stopDestination: (id: number) =>
    post<{ enabled: boolean }>(`/destinations/${id}/stop`),
  restartDestination: (id: number) =>
    post<{ status: string }>(`/destinations/${id}/restart`),
  refreshStreamKey: (id: number) =>
    post<{ destination: Destination }>(`/destinations/${id}/refresh-key`),

  // --- sources ---
  // A source is one ingested programme. Everything else -- destinations,
  // renditions, recordings -- belongs to exactly one of them. Unscoped calls
  // act on the default source, which is what keeps every other page working
  // without knowing sources exist.
  listSources: () => get<SourceView[]>("/sources"),
  getSource: (id: number) => get<SourceView>(`/sources/${id}`),
  createSource: (s: Partial<Source>) => post<SourceView>("/sources", s),
  updateSource: (id: number, s: Partial<Source>) =>
    put<SourceView>(`/sources/${id}`, s),
  deleteSource: (id: number) => del<void>(`/sources/${id}`),
  rotateSourceToken: (id: number) =>
    post<SourceView>(`/sources/${id}/token`),

  // --- renditions ---
  // A rendition is one shared video encode several destinations can select, so
  // N destinations wanting 1080p60 cost one encode rather than N. A destination
  // with no rendition is passthrough, which is the default and costs nothing.
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
  compileRouting: (profile: RoutingProfile) =>
    post<{ routing: RoutingResult; profile: RoutingProfile }>(
      "/routing/compile",
      { profile },
    ),
  listPresets: () =>
    get<{ presets: Preset[]; defaults: PresetOpts }>("/routing/presets"),
  applyPreset: (id: string, opts: PresetOpts) =>
    post<{ profile: RoutingProfile; routing: RoutingResult }>(
      `/routing/presets/${encodeURIComponent(id)}`,
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
   *  take the mixer away. */
  putAnnotations: (annotations: TrackAnnotation[]) =>
    put<{ annotations: TrackAnnotation[] | null }>("/source/annotations", {
      annotations,
    }),

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
  listProcesses: () => get<ProcessInfo[]>("/processes"),
  processLogs: (name: string) =>
    get<{ name: string; command: string; lines: LogLine[] | null }>(
      `/processes/${encodeURIComponent(name)}/logs`,
    ),

  // --- expert mode ---
  //
  // Preview and dry-run are POSTs because they carry a candidate edit in the
  // body, not because they change anything: neither writes.
  getExpert: (id: number) => get<ExpertResponse>(`/destinations/${id}/expert`),
  previewExpert: (id: number, args: ExpertArgs) =>
    post<ExpertResponse>(`/destinations/${id}/expert/preview`, args),
  dryRunExpert: (id: number, args: ExpertArgs) =>
    post<DryRunResult>(`/destinations/${id}/expert/dry-run`, args),
  putExpert: (id: number, args: ExpertArgs & { confirm: boolean }) =>
    put<ExpertResponse>(`/destinations/${id}/expert`, args),
  deleteExpert: (id: number) => del<ExpertResponse>(`/destinations/${id}/expert`),

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
  deleteCreds: (platform: string) =>
    del<{ status: string }>(`/platforms/credentials/${platform}`),
  listAccounts: () => get<PlatformAccount[]>("/platforms/accounts"),
  deleteAccount: (id: number) =>
    del<{ status: string }>(`/platforms/accounts/${id}`),
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
