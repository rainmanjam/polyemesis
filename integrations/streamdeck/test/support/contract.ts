import { readdirSync, readFileSync } from "node:fs";
import type { Method, TransportResponse } from "../../src/api.js";

// The server's own answers, recorded by internal/api/streamdeck_goldens_test.go from the real handlers on a
// seeded install, and the schema of every field each route's 2xx answer can carry. Regenerate with
//   go test ./internal/api/ -run TestTheStreamDeckContract -update-streamdeck-contract

const DIR = new URL("../contract/", import.meta.url);

export interface Golden {
  case: string;
  request: {
    method: Method;
    /** The apiContract.json path this request is an instance of. */
    route: string;
    /** The concrete path, relative to /api/v1. */
    path: string;
    query?: Record<string, string>;
    body?: unknown;
    token: "read" | "admin" | "unknown";
  };
  status: number;
  body: unknown;
}

/** One JSON value's shape: the types it may take, an object's fields, an array's items, a map's values. */
export interface Schema {
  types?: string[];
  fields?: Record<string, Schema>;
  items?: Schema;
  values?: Schema;
  any?: boolean;
}

export const GOLDENS: ReadonlyMap<string, Golden> = new Map(
  readdirSync(DIR)
    .filter((f) => f.endsWith(".json") && f !== "schema.json")
    .map((f) => {
      const g = JSON.parse(readFileSync(new URL(f, DIR), "utf8")) as Golden;
      return [g.case, g] as const;
    }),
);

/** The schema of each route's 2xx answer, keyed "METHOD /contract/path". */
export const SCHEMA: Readonly<Record<string, Schema>> = JSON.parse(readFileSync(new URL("schema.json", DIR), "utf8"));

/** A recorded case, by name. Throws for a name the goldens do not hold, so a typo fails the test. */
export function golden(name: string): Golden {
  const g = GOLDENS.get(name);
  if (!g) throw new Error(`no golden named ${name} in test/contract/`);
  return g;
}

/** A golden's answer as a transport response, with its body copied so a test may change it. */
export function goldenResponse(name: string): TransportResponse {
  const g = golden(name);
  return { status: g.status, body: JSON.stringify(g.body) };
}

/** A deep copy of a golden's body, typed by the caller. */
export function goldenBody<T>(name: string): T {
  return structuredClone(golden(name).body) as T;
}

function typeOf(v: unknown): string {
  if (v === null) return "null";
  if (Array.isArray(v)) return "array";
  return typeof v;
}

/**
 * Where a value departs from a schema: a field the server never sends, or a value of a type the server never
 * sends there. A field the server sends that the value leaves out is fine: a fake may be partial.
 */
export function departures(value: unknown, schema: Schema | undefined, path = "$"): string[] {
  if (!schema || schema.any) return [];
  const type = typeOf(value);
  if (!schema.types?.includes(type)) {
    return [`${path}: ${type}, but the server sends ${schema.types?.join(" or ") ?? "nothing"} here`];
  }
  if (type === "array") {
    return (value as unknown[]).flatMap((e, i) => departures(e, schema.items, `${path}[${i}]`));
  }
  if (type === "object") {
    const out: string[] = [];
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      if (schema.values) out.push(...departures(v, schema.values, `${path}.${k}`));
      else if (!schema.fields || !(k in schema.fields)) out.push(`${path}.${k}: not a field the server sends`);
      else out.push(...departures(v, schema.fields[k], `${path}.${k}`));
    }
    return out;
  }
  return [];
}

/** Marks a fake answer as deliberately not the server's shape, so the conformance check leaves it alone. */
export const NOT_THE_SERVERS_SHAPE = Symbol("not the server's shape");

export function malformed<T extends TransportResponse>(res: T): T {
  Object.defineProperty(res, NOT_THE_SERVERS_SHAPE, { value: true });
  return res;
}
