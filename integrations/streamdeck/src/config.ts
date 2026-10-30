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

export function keySettings(raw: Record<string, unknown> | undefined): KeySettings {
  return {
    sourceId: positiveId(raw?.sourceId),
    destinationId: positiveId(raw?.destinationId),
  };
}
