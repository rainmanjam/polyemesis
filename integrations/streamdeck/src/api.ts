import type { ConnectionInfo, Feed, LevelsReading, StatusPayload, StopImpact } from "./model.js";

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
  | "malformed";

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

  /** What a stop would end. Makes no platform call on the server. */
  stopImpact(sourceId?: number): Promise<StopImpact> {
    return this.call("GET", "/destinations/stop-impact", sourceId);
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

  private async call<T>(
    method: Method,
    path: string,
    sourceId?: number,
    body?: unknown,
  ): Promise<T> {
    const url = new URL(this.base.toString());
    url.pathname = `${this.base.pathname.replace(/\/$/, "")}/api/v1${path}`;
    url.search = "";
    if (sourceId !== undefined) url.searchParams.set("source", String(sourceId));

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
