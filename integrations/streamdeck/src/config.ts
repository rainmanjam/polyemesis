// Settings the property inspector saves. The token lives in GLOBAL settings
// only: action settings are plain text in a profile export, and a token must
// not travel with a profile. Keys carry programme and destination IDs only.

import type { StreamDeckKey } from "./keys.js";

/** A pairing or key setting that cannot be used. `key` is the translated message to show. */
export class ConfigError extends Error {
  constructor(
    readonly field: "url" | "token" | "sourceId" | "destinationId",
    readonly key: StreamDeckKey,
  ) {
    super(key);
    this.name = "ConfigError";
  }
}

export interface GlobalSettings {
  url?: string;
  token?: string;
  /** PEM of the install's CA, from GET /api/v1/tls/ca. Empty for a publicly trusted certificate. */
  caPem?: string;
}

export interface KeySettings {
  sourceId?: number;
  destinationId?: number;
  /** A routing preset id from GET /routing/presets. Not a secret: it is safe in action settings. */
  presetId?: string;
  /** A 0-based ingest track index, for the track mute key and the gain dial. Track 0 is a real track. */
  track?: number;
  /** The poll key's poll (#14). Not secrets: the question and options are posted in public chat. */
  pollQuestion?: string;
  pollOptions?: string[];
  pollDuration?: number;
}

const LOOPBACK = new Set(["localhost", "127.0.0.1", "[::1]", "::1"]);

/**
 * Parses the server address the inspector saves. https is required for any host.
 * http is allowed only on loopback, because the token is a bearer credential and
 * must not cross the network in clear text.
 */
export function parseServerUrl(raw: string | undefined): URL {
  const text = (raw ?? "").trim();
  if (text === "") throw new ConfigError("url", "streamdeck.err.urlRequired");
  let url: URL;
  try {
    url = new URL(text);
  } catch {
    throw new ConfigError("url", "streamdeck.err.urlInvalid");
  }
  if (url.protocol === "https:") return url;
  if (url.protocol === "http:" && LOOPBACK.has(url.hostname)) return url;
  throw new ConfigError("url", "streamdeck.err.urlScheme");
}

export function requireToken(raw: string | undefined): string {
  const token = (raw ?? "").trim();
  if (token === "") throw new ConfigError("token", "streamdeck.err.tokenRequired");
  return token;
}

/** A programme or destination ID is a positive integer. Anything else is dropped, not guessed. */
export function positiveId(raw: unknown): number | undefined {
  const n = typeof raw === "string" && raw.trim() !== "" ? Number(raw) : raw;
  return typeof n === "number" && Number.isSafeInteger(n) && n > 0 ? n : undefined;
}

/**
 * A track index is a non-negative integer: track 0 is the first track, so positiveId would lose it.
 * An empty string is NOT zero (Number("") is 0), so an unchosen picker saves no track at all.
 */
export function trackIndex(raw: unknown): number | undefined {
  if (typeof raw === "string" && raw.trim() === "") return undefined;
  const n = typeof raw === "string" ? Number(raw) : raw;
  return typeof n === "number" && Number.isSafeInteger(n) && n >= 0 ? n : undefined;
}

/**
 * The poll key's options: one per line in the inspector, saved as a list. Blank lines are dropped;
 * the server checks the count and lengths and says what is wrong.
 */
export function pollOptions(raw: unknown): string[] | undefined {
  const lines = Array.isArray(raw) ? raw : typeof raw === "string" ? raw.split(/\r?\n/) : [];
  const out = lines.filter((o): o is string => typeof o === "string").map((o) => o.trim()).filter((o) => o !== "");
  return out.length > 0 ? out : undefined;
}

export function keySettings(raw: Record<string, unknown> | undefined): KeySettings {
  const preset = typeof raw?.presetId === "string" ? raw.presetId.trim() : "";
  const question = typeof raw?.pollQuestion === "string" ? raw.pollQuestion.trim() : "";
  return {
    sourceId: positiveId(raw?.sourceId),
    destinationId: positiveId(raw?.destinationId),
    presetId: preset === "" ? undefined : preset,
    track: trackIndex(raw?.track),
    pollQuestion: question === "" ? undefined : question,
    pollOptions: pollOptions(raw?.pollOptions),
    pollDuration: positiveId(raw?.pollDuration),
  };
}
