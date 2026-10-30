import type {
  ConnectionInfo,
  DestinationLevels,
  Feed,
  LevelsReading,
  PresetSummary,
  RecordingState,
  StatusPayload,
  StopImpact,
  TrackLevel,
  TrackLevelResult,
} from "./model.js";
import { MAX_GAIN } from "./trackLevel.js";

// The polyemesis REST client. It knows the routes and the error taxonomy and
// nothing about Stream Deck. The transport is injected, so the tests run
// against a fake and the plugin runs against node:https (see transport.ts).

export type Method = "GET" | "POST";

export interface TransportRequest {
  method: Method;
  url: URL;
  headers: Record<string, string>;
  body?: string;
}

export interface TransportResponse {
  status: number;
  body: string;
}

/** Thrown by a transport when no HTTP response arrived at all. */
export class TransportError extends Error {
  constructor(
    readonly kind: "unreachable" | "untrusted",
    message: string,
  ) {
    super(message);
    this.name = "TransportError";
  }
}

export interface Transport {
  send(req: TransportRequest): Promise<TransportResponse>;
}

export type ApiErrorKind =
  /** The server answered 401: the token is unknown, revoked or expired. */
  | "unauthorized"
  /** The server answered 403: the token is valid but lacks the scope this route needs. */
  | "forbidden"
  /** No connection could be made at all. */
  | "unreachable"
  /** The connection was made but the certificate is not trusted. Pin the install's CA. */
  | "untrusted"
  /** Any other non-2xx answer. The server's own message is kept. */
  | "http"
  /** A 2xx answer whose body is not the JSON we expected. */
  | "malformed"
  /**
   * The install has no programme yet, so a route that acts on one refused with 503 and
   * code "no_source". The token is fine; there is simply nothing to act on.
   */
  | "noSource";

export class ApiError extends Error {
  constructor(
    readonly kind: ApiErrorKind,
    readonly status: number | undefined,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

export class PolyemesisClient {
  constructor(
    private readonly base: URL,
    private readonly token: string,
    private readonly transport: Transport,
  ) {}

  /** What this token is and may do. Answers for this token only. Any scope may call it. */
  connection(): Promise<ConnectionInfo> {
    return this.call<ConnectionInfo>("GET", "/auth/connection");
  }

  /** Destination state and the failover selector, scoped to one programme when given. */
  status(sourceId?: number): Promise<StatusPayload> {
    return this.call("GET", "/status", sourceId);
  }

  /**
   * The programme's latest levels and the server's own verdict on whether they are current.
   * Polled per programme rather than read from the WebSocket: a levels frame on /ws carries
   * no engine id, so on a multi-programme install the socket interleaves every programme.
   */
  async levels(sourceId?: number): Promise<LevelsReading> {
    const body = await this.call<{ levels?: unknown; feed?: unknown }>("GET", "/levels", sourceId);
    const feed = body.feed;
    if (feed !== "none" && feed !== "live" && feed !== "stale") {
      throw new ApiError("malformed", 200, "levels carry no feed state");
    }
    const grid = (v: unknown): v is number[][] =>
      Array.isArray(v) && v.every((row) => Array.isArray(row) && row.every((n) => typeof n === "number"));
    const raw = body.levels as { peak?: unknown; rms?: unknown } | undefined;
    const levels =
      raw && grid(raw.peak) && grid(raw.rms) ? { peak: raw.peak, rms: raw.rms } : null;
    return { levels, feed: feed as Feed };
  }

  /** The metering interval the server pushes levels at, in milliseconds. */
  async meterIntervalMs(): Promise<number> {
    const settings = await this.call<{ meters?: { intervalMs?: unknown } }>("GET", "/settings");
    const ms = settings.meters?.intervalMs;
    if (typeof ms !== "number" || !Number.isFinite(ms)) {
      throw new ApiError("malformed", 200, "settings carry no meters.intervalMs");
    }
    return ms;
  }

  /** What a stop of the whole programme would end. Makes no platform call on the server. */
  stopImpact(sourceId?: number): Promise<StopImpact> {
    return this.call("GET", "/destinations/stop-impact", sourceId);
  }

  /**
   * What stopping ONE destination would end. With a programme, the server also checks the
   * destination is on it. It answers 404 for an id it does not hold, or one on another
   * programme, and the key treats either as a refusal.
   */
  stopImpactFor(destinationId: number, sourceId?: number): Promise<StopImpact> {
    return this.call("GET", "/destinations/stop-impact", sourceId, undefined, {
      destination: String(destinationId),
    });
  }

  /** Whether recording is on for the install, and what the recorder is doing. */
  recording(): Promise<RecordingState> {
    return this.call("GET", "/recording");
  }

  /** Switches recording on or off for the whole install. Needs an admin token. */
  setRecording(enabled: boolean): Promise<RecordingState> {
    return this.call("POST", "/recording", undefined, { enabled });
  }

  /** The routing presets this install offers. Needs no programme. */
  async routingPresets(): Promise<PresetSummary[]> {
    const body = await this.call<{ presets?: unknown }>("GET", "/routing/presets");
    const presets = body.presets;
    const isSummary = (p: unknown): p is PresetSummary =>
      typeof p === "object" &&
      p !== null &&
      typeof (p as PresetSummary).id === "string" &&
      typeof (p as PresetSummary).name === "string";
    if (!Array.isArray(presets) || !presets.every(isSummary)) {
      throw new ApiError("malformed", 200, "routing presets carry no id and name");
    }
    return presets.map((p) => ({ id: p.id, name: p.name }));
  }

  /** Applies a routing preset to one programme with the server's default options. Can restart its destinations. */
  applyPreset(presetId: string, sourceId?: number): Promise<unknown> {
    return this.call("POST", `/routing/presets/${encodeURIComponent(presetId)}`, sourceId);
  }

  startAll(sourceId?: number): Promise<unknown> {
    return this.call("POST", "/destinations/start-all", sourceId);
  }

  /** Stop-all ends every YouTube broadcast it touches. The server refuses without confirm. */
  stopAll(sourceId?: number): Promise<unknown> {
    return this.call("POST", "/destinations/stop-all", sourceId, { confirm: true });
  }

  startDestination(id: number, sourceId?: number): Promise<unknown> {
    return this.call("POST", `/destinations/${id}/start`, sourceId);
  }

  stopDestination(id: number, sourceId?: number): Promise<unknown> {
    return this.call("POST", `/destinations/${id}/stop`, sourceId);
  }

  /** Manual failover to a named source, e.g. "backup" or "slate". */
  switchSource(source: string, sourceId?: number): Promise<unknown> {
    return this.call("POST", "/failover/source", sourceId, { source });
  }

  /**
   * Every destination's stored track levels. The server ignores ?source= on this list, so it is
   * one install-wide read; a key picks its own row and checks the programme itself.
   */
  async destinations(): Promise<DestinationLevels[]> {
    const body = await this.call<unknown>("GET", "/destinations");
    if (!Array.isArray(body)) throw new ApiError("malformed", 200, "the destination list is not a list");
    return body.map((item) => {
      const parsed = parseDestinationLevels((item as { destination?: unknown } | null)?.destination);
      if (!parsed) throw new ApiError("malformed", 200, "a destination carries no id, name or track levels");
      return parsed;
    });
  }

  /**
   * The names an operator knows a programme's tracks by, per 0-based track: the label set in
   * the UI, else the title the container carried. A track with neither is left out, and the
   * caller names it by number.
   */
  async trackNames(sourceId?: number): Promise<Map<number, string>> {
    const body = await this.call<{ tracks?: unknown; annotations?: unknown }>("GET", "/source", sourceId);
    const names = new Map<number, string>();
    if (Array.isArray(body.tracks)) {
      for (const t of body.tracks as { index?: unknown; title?: unknown }[]) {
        if (Number.isSafeInteger(t?.index) && typeof t.title === "string" && t.title.trim() !== "") {
          names.set(t.index as number, t.title.trim());
        }
      }
    }
    if (Array.isArray(body.annotations)) {
      for (const a of body.annotations as { track?: unknown; label?: unknown }[]) {
        if (Number.isSafeInteger(a?.track) && typeof a.label === "string" && a.label.trim() !== "") {
          names.set(a.track as number, a.label.trim());
        }
      }
    }
    return new Map([...names.entries()].sort(([a], [b]) => a - b));
  }

  /**
   * Mutes, unmutes or sets the gain of one track on one destination. Needs an admin token.
   * A gain the server would refuse (zero, negative, NaN, Inf, above its cap) is refused HERE,
   * before any request: the dial clamps, and this is the last guard.
   */
  async setTrackLevel(
    destinationId: number,
    track: number,
    change: { muted?: boolean; gain?: number },
    sourceId?: number,
  ): Promise<TrackLevelResult> {
    if (!Number.isSafeInteger(track) || track < 0) throw new RangeError(`track ${track} is not a track index`);
    if (change.muted === undefined && change.gain === undefined) throw new RangeError("nothing to change");
    if (change.gain !== undefined && !(Number.isFinite(change.gain) && change.gain > 0 && change.gain <= MAX_GAIN)) {
      throw new RangeError(`gain ${change.gain} is outside (0, ${MAX_GAIN}]`);
    }
    const body: { muted?: boolean; gain?: number } = {};
    if (change.muted !== undefined) body.muted = change.muted;
    if (change.gain !== undefined) body.gain = change.gain;
    const out = await this.call<{ applied?: unknown; reason?: unknown; destination?: unknown }>(
      "POST",
      `/destinations/${destinationId}/tracks/${track}/gain`,
      sourceId,
      body,
    );
    if (out?.applied !== "live" && out?.applied !== "restart") {
      throw new ApiError("malformed", 200, "the answer does not say how the level applied");
    }
    const result: TrackLevelResult = { applied: out.applied };
    if (typeof out.reason === "string" && out.reason !== "") result.reason = out.reason;
    const destination = parseDestinationLevels(out.destination);
    if (destination) result.destination = destination;
    return result;
  }

  private async call<T>(
    method: Method,
    path: string,
    sourceId?: number,
    body?: unknown,
    query?: Record<string, string>,
  ): Promise<T> {
    const url = new URL(this.base.toString());
    url.pathname = `${this.base.pathname.replace(/\/$/, "")}/api/v1${path}`;
    url.search = "";
    if (sourceId !== undefined) url.searchParams.set("source", String(sourceId));
    for (const [name, value] of Object.entries(query ?? {})) url.searchParams.set(name, value);

    const headers: Record<string, string> = {
      Authorization: `Bearer ${this.token}`,
      Accept: "application/json",
    };
    const payload = body === undefined ? undefined : JSON.stringify(body);
    if (payload !== undefined) headers["Content-Type"] = "application/json";

    let res: TransportResponse;
    try {
      res = await this.transport.send({ method, url, headers, body: payload });
    } catch (err) {
      if (err instanceof TransportError) {
        throw new ApiError(err.kind, undefined, err.message);
      }
      throw new ApiError("unreachable", undefined, err instanceof Error ? err.message : String(err));
    }

    if (res.status === 401) throw new ApiError("unauthorized", 401, serverMessage(res.body, "unauthorized"));
    if (res.status === 403) throw new ApiError("forbidden", 403, serverMessage(res.body, "forbidden"));
    if (res.status === 503 && errorCode(res.body) === "no_source") {
      throw new ApiError("noSource", 503, serverMessage(res.body, "no source"));
    }
    if (res.status < 200 || res.status >= 300) {
      throw new ApiError("http", res.status, serverMessage(res.body, `HTTP ${res.status}`));
    }
    if (res.body.trim() === "") return undefined as T;
    try {
      return JSON.parse(res.body) as T;
    } catch {
      throw new ApiError("malformed", res.status, "the response was not JSON");
    }
  }
}

/** The machine-readable code the server sends beside its message, when it sends one. */
function errorCode(body: string): string | undefined {
  try {
    const parsed = JSON.parse(body) as { code?: unknown };
    return typeof parsed.code === "string" ? parsed.code : undefined;
  } catch {
    return undefined;
  }
}

/** The server's {"error": "..."} message when it sent one, else the fallback. */
function serverMessage(body: string, fallback: string): string {
  try {
    const parsed = JSON.parse(body) as { error?: unknown };
    if (typeof parsed.error === "string" && parsed.error !== "") return parsed.error;
  } catch {
    // Not JSON: the fallback is the honest answer.
  }
  return fallback;
}

/**
 * The part of a stored destination a track-level control needs, or undefined when the row is not
 * the server's shape. Nothing else is copied: the row carries publish credentials for an admin token.
 */
function parseDestinationLevels(raw: unknown): DestinationLevels | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const d = raw as {
    id?: unknown;
    name?: unknown;
    kind?: unknown;
    sourceId?: unknown;
    audio?: { copy?: unknown } | null;
    profile?: { mode?: unknown; tracks?: unknown } | null;
  };
  if (!Number.isSafeInteger(d.id) || typeof d.name !== "string") return undefined;
  const rawTracks = d.profile?.tracks ?? [];
  if (!Array.isArray(rawTracks)) return undefined;
  const tracks: TrackLevel[] = [];
  for (const t of rawTracks as { track?: unknown; enabled?: unknown; gain?: unknown; muted?: unknown }[]) {
    if (!Number.isSafeInteger(t?.track) || typeof t.gain !== "number" || !Number.isFinite(t.gain)) return undefined;
    tracks.push({ track: t.track as number, enabled: t.enabled === true, gain: t.gain, muted: t.muted === true });
  }
  const out: DestinationLevels = {
    id: d.id as number,
    name: d.name,
    matrix: d.profile?.mode === "matrix",
    // An audio-only destination (an Icecast mount) has its own copy rule on the server, and it is mixed.
    copyAudio: d.audio?.copy === true && d.kind !== "audio",
    tracks,
  };
  if (Number.isSafeInteger(d.sourceId)) out.sourceId = d.sourceId as number;
  return out;
}
