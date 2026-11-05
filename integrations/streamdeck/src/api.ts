import type {
  ConnectionInfo,
  DestinationLevels,
  DumpAllRow,
  DumpResult,
  Feed,
  LevelsReading,
  PollSpec,
  PollSummary,
  PresetApplyResult,
  PresetApplyRow,
  PresetSummary,
  RecordingState,
  StatusPayload,
  StopImpact,
  TrackLevel,
  TrackLevelResult,
  ViewersLive,
} from "./model.js";
import { MAX_GAIN } from "./trackLevel.js";
import contractFile from "./apiContract.json" with { type: "json" };

// The polyemesis REST client. It knows the routes and the error taxonomy and
// nothing about Stream Deck. The transport is injected, so the tests run
// against a fake and the plugin runs against node:https (see transport.ts).

export type Method = "GET" | "POST";

/** The token scope a route needs on the server. */
export type Scope = "read" | "admin";

/** One route the plugin calls, from apiContract.json. */
export interface ContractRoute {
  readonly method: Method;
  /** Relative to /api/v1. A {name} segment stands for one path segment. */
  readonly path: string;
  readonly scope: Scope;
  /**
   * An admin route the plugin still sends on a read token, to paint the server's 403 as Needs admin token.
   * Every other admin route is refused here on a read token, before any request.
   */
  readonly readTokenSends: boolean;
}

/**
 * Every route the plugin may call, and the scope the server needs for it (apiContract.json). call() refuses a
 * route that is not listed, and the Go contract test (internal/api/streamdeck_contract_test.go) drives each
 * entry through the real router, so the list cannot drift from the server or from this file's call() sites.
 */
export const API_CONTRACT: readonly ContractRoute[] = contractFile.routes.map((r): ContractRoute => {
  const raw = r as { method?: unknown; path?: unknown; scope?: unknown; readTokenSends?: unknown };
  if (raw.method !== "GET" && raw.method !== "POST") throw new Error(`apiContract.json: ${String(raw.method)} is not GET or POST`);
  if (typeof raw.path !== "string" || !raw.path.startsWith("/")) throw new Error(`apiContract.json: ${String(raw.path)} is not a path`);
  if (raw.scope !== "read" && raw.scope !== "admin") throw new Error(`apiContract.json: ${raw.path} has scope ${String(raw.scope)}`);
  if (raw.readTokenSends !== undefined && (raw.readTokenSends !== true || raw.scope !== "admin")) {
    throw new Error(`apiContract.json: ${raw.path}: readTokenSends is only for an admin route, and only true`);
  }
  return { method: raw.method, path: raw.path, scope: raw.scope, readTokenSends: raw.readTokenSends === true };
});

/** The contract entry a request matches, segment by segment, or undefined when the plugin has none for it. */
export function contractRoute(method: Method, path: string): ContractRoute | undefined {
  const want = path.split("/");
  return API_CONTRACT.find((r) => {
    if (r.method !== method) return false;
    const have = r.path.split("/");
    return have.length === want.length && have.every((seg, i) => (/^\{\w+\}$/.test(seg) ? want[i] !== "" : seg === want[i]));
  });
}

export interface TransportRequest {
  method: Method;
  url: URL;
  headers: Record<string, string>;
  body?: string;
  /** How long to wait for the answer. Unset: the transport's own default. */
  timeoutMs?: number;
}

/**
 * How long the two dump calls wait. The server holds a dump's answer until the stream resumes at the
 * next keyframe, up to 12 s; a transport that gave up first would make a dump that went through look
 * failed, and the operator would hold again.
 */
export const DUMP_TIMEOUT_MS = 20_000;

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
  /** The token's scope, once connection() has answered. Until then nothing is refused here. */
  private scope: Scope | undefined;

  constructor(
    private readonly base: URL,
    private readonly token: string,
    private readonly transport: Transport,
  ) {}

  /** What this token is and may do. Answers for this token only. Any scope may call it. */
  async connection(): Promise<ConnectionInfo> {
    const info = await this.call<ConnectionInfo>("GET", "/auth/connection");
    // Anything that is not admin is read-only on the server, a scope this build does not know included.
    this.scope = info?.adminScope === true ? "admin" : "read";
    return info;
  }

  /** The install's programmes, ids only. Install-wide: never scoped by ?source=. */
  async sources(): Promise<{ id: number }[]> {
    const body = await this.call<unknown>("GET", "/sources");
    if (!Array.isArray(body)) throw new ApiError("malformed", 200, "the programme list is not a list");
    return body.map((row) => {
      const id = (row as { id?: unknown } | null)?.id;
      if (!Number.isSafeInteger(id)) throw new ApiError("malformed", 200, "a programme carries no id");
      return { id: id as number };
    });
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

  /** The running chat poll, or null when none is. Any scope may read it: it is counts only. */
  async currentPoll(): Promise<PollSummary | null> {
    const body = await this.call<{ current?: unknown }>("GET", "/polls", undefined, undefined, { limit: "1" });
    const c = body?.current as Partial<PollSummary> | null | undefined;
    if (c === null || c === undefined) return null;
    if (!Number.isSafeInteger(c.id) || typeof c.state !== "string" || typeof c.endsAt !== "string") {
      throw new ApiError("malformed", 200, "the poll was not the expected shape");
    }
    return {
      id: c.id as number,
      question: typeof c.question === "string" ? c.question : "",
      state: c.state,
      total: typeof c.total === "number" ? c.total : 0,
      endsAt: c.endsAt,
    };
  }

  /**
   * The cross-platform viewer count (Wave 4 #4). Needs an admin token: the server refuses a read token with
   * 403, because the answer names the operator's connected channels. A count a platform did not report is
   * left absent, never read as 0.
   */
  async analyticsLive(): Promise<ViewersLive> {
    const body = await this.call<Record<string, unknown>>("GET", "/analytics/live");
    if (!body || typeof body !== "object" || typeof body.onAir !== "boolean") {
      throw new ApiError("malformed", 200, "the audience was not the expected shape");
    }
    const count = (v: unknown) => (typeof v === "number" && Number.isFinite(v) && v >= 0 ? v : undefined);
    const readings = Array.isArray(body.readings) ? body.readings : [];
    return {
      enabled: body.enabled === true,
      onAir: body.onAir,
      total: count(body.total),
      partial: body.partial === true,
      readings: readings
        .filter((r): r is Record<string, unknown> => !!r && typeof r === "object" && typeof (r as { platform?: unknown }).platform === "string")
        .map((r) => ({ platform: r.platform as string, viewers: count(r.viewers) })),
    };
  }

  /** Starts a chat poll, announced in every chat. Needs an admin token. */
  startPoll(spec: PollSpec): Promise<unknown> {
    return this.call("POST", "/polls", undefined, spec);
  }

  /** Ends the running poll early. Needs an admin token. */
  endPoll(id: number): Promise<unknown> {
    return this.call("POST", `/polls/${id}/end`);
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

  /**
   * Applies a routing preset to every destination of one programme, with the pickers following the programme's
   * track roles. The server stores it as a routing-editor save does and restarts only the destinations whose
   * graph it changes. POST /routing/presets/{preset} (no /apply) only compiles a preset and stores nothing.
   */
  async applyPreset(presetId: string, sourceId?: number): Promise<PresetApplyResult> {
    const out = await this.call<{ results?: unknown; reconcileFailed?: unknown }>(
      "POST",
      `/routing/presets/${encodeURIComponent(presetId)}/apply`,
      sourceId,
    );
    if (!Array.isArray(out?.results)) throw new ApiError("malformed", 200, "the preset answer carries no results");
    const results = (out.results as Record<string, unknown>[]).map((r) => {
      // An outcome this build does not know is not counted as saved.
      const outcome = r?.outcome === "saved" || r?.outcome === "skipped" ? r.outcome : "failed";
      const row: PresetApplyRow = {
        id: Number.isSafeInteger(r?.id) ? (r.id as number) : 0,
        name: typeof r?.name === "string" ? r.name : "",
        outcome,
      };
      if (r?.applied === "live" || r?.applied === "restart") row.applied = r.applied;
      if (typeof r?.message === "string" && r.message !== "") row.message = r.message;
      return row;
    });
    return { results, reconcileFailed: out.reconcileFailed === true };
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

  /**
   * The console's Retry: restarts one destination's output. On a failed destination it starts a fresh
   * attempt, and on one held after repeated quick rejections it lifts the hold (a new supervise loop
   * clears the rejection count and the reason). It interrupts a running output too, so the runtime only
   * sends it for a destination that has failed. Needs an admin token.
   */
  restartDestination(id: number, sourceId?: number): Promise<unknown> {
    return this.call("POST", `/destinations/${id}/restart`, sourceId);
  }

  /**
   * Stops one destination. With `now`, a delayed destination's buffered tail is CUT (?now=true): nothing
   * still buffered airs, and a tail already airing from an earlier stop is cut too.
   */
  stopDestination(id: number, sourceId?: number, opts: { now?: boolean } = {}): Promise<unknown> {
    return this.call("POST", `/destinations/${id}/stop`, sourceId, undefined, opts.now ? { now: "true" } : undefined);
  }

  /**
   * Drops the newest part of one delayed destination's buffer; the stream keeps running and the delay
   * stays shorter until the destination next starts. With no seconds the server uses the destination's
   * own default (delayDumpSeconds), else everything since the last dump point goes. It answers only once
   * the stream resumes at the next keyframe, hence DUMP_TIMEOUT_MS, and 409 when there is nothing to
   * dump. Needs an admin token.
   */
  async dumpDelay(id: number, seconds?: number, sourceId?: number): Promise<DumpResult> {
    const out = await this.call<{ id?: unknown; name?: unknown; dumpedSeconds?: unknown }>(
      "POST",
      `/destinations/${id}/delay/dump`,
      sourceId,
      seconds === undefined ? undefined : { seconds },
      undefined,
      DUMP_TIMEOUT_MS,
    );
    return {
      id: Number.isSafeInteger(out?.id) ? (out.id as number) : id,
      name: typeof out?.name === "string" ? out.name : "",
      dumpedSeconds: typeof out?.dumpedSeconds === "number" && Number.isFinite(out.dumpedSeconds) ? out.dumpedSeconds : 0,
    };
  }

  /**
   * Dumps every enabled, delayed destination of the programme. The server refuses without confirm, and
   * refuses a call with no programme on a multi-programme install unless the body says allProgrammes:
   * the deck never sends that, so a key with no programme only works on a single-programme install.
   */
  async dumpAll(sourceId?: number, seconds?: number): Promise<DumpAllRow[]> {
    const body: { confirm: true; seconds?: number } = { confirm: true };
    if (seconds !== undefined) body.seconds = seconds;
    const out = await this.call<{ results?: unknown }>(
      "POST",
      "/destinations/delay/dump-all",
      sourceId,
      body,
      undefined,
      DUMP_TIMEOUT_MS,
    );
    if (!Array.isArray(out?.results)) throw new ApiError("malformed", 200, "the dump answer carries no results");
    return (out.results as Record<string, unknown>[]).map((r) => {
      const outcome = r?.outcome === "dumped" || r?.outcome === "skipped" ? r.outcome : "failed";
      const row: DumpAllRow = {
        id: Number.isSafeInteger(r?.id) ? (r.id as number) : 0,
        name: typeof r?.name === "string" ? r.name : "",
        outcome,
      };
      if (typeof r?.message === "string" && r.message !== "") row.message = r.message;
      if (typeof r?.dumpedSeconds === "number" && Number.isFinite(r.dumpedSeconds)) row.dumpedSeconds = r.dumpedSeconds;
      return row;
    });
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
    timeoutMs?: number,
  ): Promise<T> {
    const route = contractRoute(method, path);
    if (!route) {
      // A programming error, not an answer from the install: the route is missing from apiContract.json.
      throw new Error(`${method} ${path} is not in apiContract.json; list it there with the scope the server needs`);
    }
    if (route.scope === "admin" && !route.readTokenSends && this.scope === "read") {
      throw new ApiError("forbidden", undefined, `${method} ${route.path} needs an admin token; a read token sends nothing`);
    }
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
      const req: TransportRequest = { method, url, headers, body: payload };
      if (timeoutMs !== undefined) req.timeoutMs = timeoutMs;
      res = await this.transport.send(req);
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
