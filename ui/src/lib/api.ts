import type {
  ApiToken,
  Destination,
  DiskUsage,
  EncoderList,
  Levels,
  PlatformAccount,
  PlatformCreds,
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
  Settings,
  SetupGuide,
  SourceInfo,
  Status,
  SystemInfo,
  SystemStats,
  TlsStatus,
  BitrateSample,
  LogLine,
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
const del = <T,>(p: string) => request<T>(p, { method: "DELETE" });

export interface DestinationWithRouting {
  destination: Destination;
  routing?: RoutingResult;
  routingError?: string;
}

export const api = {
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
  putSettings: (s: Settings) => put<Settings>("/settings", s),

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
  createDestination: (d: Partial<Destination>) =>
    post<{ destination: Destination }>("/destinations", d),
  updateDestination: (id: number, d: Partial<Destination>) =>
    put<DestinationWithRouting>(`/destinations/${id}`, d),
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
  compileRouting: (profile: RoutingProfile) =>
    post<{ routing: RoutingResult; profile: RoutingProfile }>(
      "/routing/compile",
      { profile },
    ),
  listPresets: () =>
    get<{ presets: Preset[]; defaults: PresetOpts }>("/routing/presets"),
  applyPreset: (id: string, opts: PresetOpts) =>
    post<{ profile: RoutingProfile; routing: RoutingResult }>(
      `/routing/presets/${id}`,
      opts,
    ),

  // --- recordings ---
  listRecordings: () => get<Recording[]>("/recordings"),
  recordingUsage: () => get<DiskUsage>("/recordings/usage"),
  deleteRecording: (id: number) => del<{ status: string }>(`/recordings/${id}`),
  downloadUrl: (id: number) => `${BASE}/recordings/${id}/download`,

  // --- monitoring ---
  listProcesses: () => get<ProcessInfo[]>("/processes"),
  processLogs: (name: string) =>
    get<{ name: string; command: string; lines: LogLine[] | null }>(
      `/processes/${encodeURIComponent(name)}/logs`,
    ),

  // --- platforms ---
  platformGuides: () => get<SetupGuide[]>("/platforms/guides"),
  listCreds: () => get<PlatformCreds[]>("/platforms/credentials"),
  putCreds: (platform: string, clientId: string, clientSecret: string) =>
    put<{ platform: string }>(`/platforms/credentials/${platform}`, {
      clientId,
      clientSecret,
    }),
  deleteCreds: (platform: string) =>
    del<{ status: string }>(`/platforms/credentials/${platform}`),
  listAccounts: () => get<PlatformAccount[]>("/platforms/accounts"),
  deleteAccount: (id: number) =>
    del<{ status: string }>(`/platforms/accounts/${id}`),
  /** A full-page navigation, not XHR: the platform's consent screen owns the tab. */
  connectUrl: (platform: string) => `${BASE}/oauth/${platform}/start`,
};
