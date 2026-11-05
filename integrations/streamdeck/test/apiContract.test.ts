import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import {
  API_CONTRACT,
  ApiError,
  PolyemesisClient,
  contractRoute,
  type Method,
  type Transport,
  type TransportRequest,
} from "../src/api.js";

// apiContract.json is the plugin's list of routes and the scope each needs. This file holds it to the
// call() sites in src/api.ts, statically, so a call nobody exercises in a test still has to be listed; and
// it holds the client to the scope column. The server side is internal/api/streamdeck_contract_test.go,
// which parses the same call() sites the same way and drives each one through the real router.

interface CallSite {
  method: Method;
  /** The path with each ${expr} written {name}. */
  path: string;
  line: number;
}

/**
 * Every this.call(...) in the source, parsed strictly: an optional <...> type argument, then a "GET" or
 * "POST" literal, then a string or template literal path whose ${} holds a bare identifier or
 * encodeURIComponent(identifier). Anything else throws, so a call written another way cannot slip past.
 */
function parseCallSites(src: string): CallSite[] {
  const lineOf = (i: number) => src.slice(0, i).split("\n").length;
  const fail = (i: number, why: string): never => {
    throw new Error(`src/api.ts:${lineOf(i)}: ${why}`);
  };
  const skipWs = (i: number) => {
    while (i < src.length && /\s/.test(src[i]!)) i++;
    return i;
  };
  const out: CallSite[] = [];
  // Every token "call" followed by "<" or "(": the definition and the call sites. Nothing else may exist,
  // except on a comment line (one that starts with // or a doc comment's *), where prose may name call().
  // Not /*: code can follow a block comment on its line.
  for (const m of src.matchAll(/\bcall\s*[<(]/g)) {
    const at = m.index;
    const lineStart = src.lastIndexOf("\n", at) + 1;
    if (/^\s*(\/\/|\*)/.test(src.slice(lineStart, at))) continue;
    if (/private\s+async\s+$/.test(src.slice(Math.max(0, at - 32), at))) continue; // the definition
    if (src.slice(at - 5, at) !== "this.") fail(at, "a call( that is not this.call(");
    let i = skipWs(at + "call".length);
    if (src[i] === "<") {
      let depth = 0;
      for (; i < src.length; i++) {
        if (src[i] === "<") depth++;
        else if (src[i] === ">" && --depth === 0) break;
      }
      i = skipWs(i + 1);
    }
    if (src[i] !== "(") fail(i, "this.call is not followed by (");
    i = skipWs(i + 1);
    const verb = /^"(GET|POST)"/.exec(src.slice(i));
    if (!verb) fail(i, 'the method is not a "GET" or "POST" literal');
    i = skipWs(i + verb![0].length);
    if (src[i] !== ",") fail(i, "no path after the method");
    i = skipWs(i + 1);
    const quote = src[i];
    if (quote !== '"' && quote !== "`") fail(i, "the path is not a string or template literal");
    let path = "";
    for (i++; src[i] !== quote; i++) {
      if (i >= src.length || src[i] === "\n" || src[i] === "\\") fail(i, "an unterminated or escaped path");
      if (quote === "`" && src[i] === "$" && src[i + 1] === "{") {
        const end = src.indexOf("}", i);
        const expr = src.slice(i + 2, end).trim();
        const name = /^(?:encodeURIComponent\(\s*(\w+)\s*\)|(\w+))$/.exec(expr);
        if (!name) fail(i, `the path segment \${${expr}} is not an identifier`);
        path += `{${name![1] ?? name![2]}}`;
        i = end;
        continue;
      }
      path += src[i];
    }
    out.push({ method: verb![1] as Method, path, line: lineOf(at) });
  }
  return out;
}

const SOURCE = readFileSync(new URL("../src/api.ts", import.meta.url), "utf8");

describe("apiContract.json against src/api.ts", () => {
  const sites = parseCallSites(SOURCE);

  it("parses every this.call in the client", () => {
    // The count is the backstop for the parser itself: a this.call it skipped would be missing here.
    expect(sites.length).toBe([...SOURCE.matchAll(/\bthis\.call\b/g)].length);
    expect(sites.length).toBeGreaterThanOrEqual(25);
  });

  it("lists every route a call() site sends, under the same path", () => {
    const unlisted = sites.filter((s) => !API_CONTRACT.some((r) => r.method === s.method && r.path === s.path));
    expect(unlisted.map((s) => `src/api.ts:${s.line} ${s.method} ${s.path}`)).toEqual([]);
  });

  it("lists no route the client never calls", () => {
    const stale = API_CONTRACT.filter((r) => !sites.some((s) => s.method === r.method && s.path === r.path));
    expect(stale.map((r) => `${r.method} ${r.path}`)).toEqual([]);
  });

  it("lists each route once", () => {
    const keys = API_CONTRACT.map((r) => `${r.method} ${r.path}`);
    expect(new Set(keys).size).toBe(keys.length);
  });

  it("refuses to parse a call written another way", () => {
    expect(() => parseCallSites('this.call(method, "/status")')).toThrow(/literal/);
    expect(() => parseCallSites("this.call(\"GET\", `/destinations/${dest.id}/start`)")).toThrow(/identifier/);
    expect(() => parseCallSites('client.call("GET", "/status")')).toThrow(/not this.call/);
    expect(parseCallSites('// call() is documented here\n * and call(here)\n')).toEqual([]);
    expect(() => parseCallSites('/* a note */ client.call("GET", "/status")')).toThrow(/not this.call/);
    expect(parseCallSites('this.call<Record<string, unknown>>(\n  "POST",\n  `/polls/${id}/end`)')).toEqual([
      { method: "POST", path: "/polls/{id}/end", line: 1 },
    ]);
  });
});

describe("contractRoute", () => {
  it("matches a concrete path segment by segment", () => {
    expect(contractRoute("POST", "/destinations/7/restart")?.path).toBe("/destinations/{id}/restart");
    expect(contractRoute("POST", "/destinations/7/tracks/0/gain")?.path).toBe("/destinations/{destinationId}/tracks/{track}/gain");
    expect(contractRoute("POST", "/destinations/start-all")?.path).toBe("/destinations/start-all");
    expect(contractRoute("GET", "/destinations/7/restart")).toBeUndefined();
    expect(contractRoute("POST", "/destinations//restart")).toBeUndefined();
    expect(contractRoute("POST", "/destinations/7/restart/now")).toBeUndefined();
  });
});

describe("PolyemesisClient: the contract's scope column", () => {
  function fake() {
    const sent: TransportRequest[] = [];
    const transport: Transport = {
      async send(req) {
        sent.push(req);
        if (req.url.pathname.endsWith("/auth/connection")) {
          return { status: 200, body: JSON.stringify({ name: "Stream Deck", prefix: "pmk", scope: "read", adminScope: false }) };
        }
        return { status: 403, body: JSON.stringify({ error: "this API token is read-only" }) };
      },
    };
    return { client: new PolyemesisClient(new URL("https://studio.example"), "t", transport), sent };
  }

  it("on a read token, sends no admin route the plugin gates, and sends the one it paints", async () => {
    const { client, sent } = fake();
    await client.connection();
    const refused = await client.startDestination(7, 1).catch((e: unknown) => e);
    expect(refused).toBeInstanceOf(ApiError);
    expect((refused as ApiError).kind).toBe("forbidden");
    expect(sent.map((r) => r.url.pathname)).toEqual(["/api/v1/auth/connection"]);

    const painted = await client.analyticsLive().catch((e: unknown) => e);
    expect((painted as ApiError).kind).toBe("forbidden");
    expect((painted as ApiError).status).toBe(403);
    expect(sent.map((r) => r.url.pathname)).toEqual(["/api/v1/auth/connection", "/api/v1/analytics/live"]);
  });

  it("before the connection check, refuses nothing: the server decides", async () => {
    const { client, sent } = fake();
    await client.startDestination(7, 1).catch(() => undefined);
    expect(sent.map((r) => r.url.pathname)).toEqual(["/api/v1/destinations/7/start"]);
  });

  it("throws, sending nothing, for a route the contract does not list", async () => {
    const { client, sent } = fake();
    const call = (client as unknown as { call(m: Method, p: string): Promise<unknown> }).call.bind(client);
    await expect(call("GET", "/auth/tokens")).rejects.toThrow(/not in apiContract.json/);
    expect(sent).toEqual([]);
  });

  it("marks readTokenSends only on admin routes", () => {
    expect(API_CONTRACT.filter((r) => r.readTokenSends).every((r) => r.scope === "admin")).toBe(true);
  });
});
