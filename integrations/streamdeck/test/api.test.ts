import { describe, expect, it } from "vitest";
import {
  ApiError,
  PolyemesisClient,
  TransportError,
  type Transport,
  type TransportRequest,
  type TransportResponse,
} from "../src/api.js";
import { classify } from "../src/transport.js";

type Reply = TransportResponse | Error;

/** A transport that records every request and answers from a queue. */
function fakeTransport(replies: Reply[] = []) {
  const sent: TransportRequest[] = [];
  const transport: Transport = {
    async send(req) {
      sent.push(req);
      const next = replies.shift() ?? { status: 200, body: "{}" };
      if (next instanceof Error) throw next;
      return next;
    },
  };
  return { transport, sent };
}

const BASE = new URL("https://studio.example:443");
const ok = (body: unknown): Reply => ({ status: 200, body: JSON.stringify(body) });

describe("PolyemesisClient: requests", () => {
  it("sends the bearer token and reads the connection answer", async () => {
    const { transport, sent } = fakeTransport([
      ok({ name: "Stream Deck", prefix: "pmk_abc", scope: "read", adminScope: false }),
    ]);
    const info = await new PolyemesisClient(BASE, "secret-token", transport).connection();
    expect(info).toEqual({ name: "Stream Deck", prefix: "pmk_abc", scope: "read", adminScope: false });
    expect(sent[0]!.method).toBe("GET");
    expect(sent[0]!.url.pathname).toBe("/api/v1/auth/connection");
    expect(sent[0]!.headers.Authorization).toBe("Bearer secret-token");
  });

  it("scopes a status read to one programme with ?source=", async () => {
    const { transport, sent } = fakeTransport([ok({ destinations: [] })]);
    await new PolyemesisClient(BASE, "t", transport).status(7);
    expect(sent[0]!.url.pathname).toBe("/api/v1/status");
    expect(sent[0]!.url.searchParams.get("source")).toBe("7");
  });

  it("omits ?source= when the key is not bound to a programme", async () => {
    const { transport, sent } = fakeTransport([ok({ destinations: [] })]);
    await new PolyemesisClient(BASE, "t", transport).status();
    expect(sent[0]!.url.search).toBe("");
  });

  it("sends stop-all with confirm: true in the body, scoped to the programme", async () => {
    const { transport, sent } = fakeTransport([ok({ results: [] })]);
    await new PolyemesisClient(BASE, "t", transport).stopAll(3);
    expect(sent[0]!.method).toBe("POST");
    expect(sent[0]!.url.pathname).toBe("/api/v1/destinations/stop-all");
    expect(sent[0]!.url.searchParams.get("source")).toBe("3");
    expect(JSON.parse(sent[0]!.body!)).toEqual({ confirm: true });
  });

  it("sends start-all with no body", async () => {
    const { transport, sent } = fakeTransport([ok({})]);
    await new PolyemesisClient(BASE, "t", transport).startAll(3);
    expect(sent[0]!.body).toBeUndefined();
    expect(sent[0]!.url.pathname).toBe("/api/v1/destinations/start-all");
  });

  it("addresses one destination's start and stop by id", async () => {
    const { transport, sent } = fakeTransport([ok({}), ok({})]);
    const client = new PolyemesisClient(BASE, "t", transport);
    await client.startDestination(12, 3);
    await client.stopDestination(12, 3);
    expect(sent.map((r) => r.url.pathname)).toEqual(["/api/v1/destinations/12/start", "/api/v1/destinations/12/stop"]);
  });

  it("reads the stop impact", async () => {
    const { transport, sent } = fakeTransport([ok({ destinations: 3, youtubeBroadcasts: 1 })]);
    const impact = await new PolyemesisClient(BASE, "t", transport).stopImpact(3);
    expect(impact).toEqual({ destinations: 3, youtubeBroadcasts: 1 });
    expect(sent[0]!.url.pathname).toBe("/api/v1/destinations/stop-impact");
  });

  it("switches the failover source by name", async () => {
    const { transport, sent } = fakeTransport([ok({})]);
    await new PolyemesisClient(BASE, "t", transport).switchSource("backup", 3);
    expect(sent[0]!.url.pathname).toBe("/api/v1/failover/source");
    expect(JSON.parse(sent[0]!.body!)).toEqual({ source: "backup" });
  });

  it("keeps a path prefix on the install's base address", async () => {
    const { transport, sent } = fakeTransport([ok({ destinations: [] })]);
    await new PolyemesisClient(new URL("https://host.example/pm/"), "t", transport).status();
    expect(sent[0]!.url.pathname).toBe("/pm/api/v1/status");
  });

  it("reads the metering interval from settings", async () => {
    const { transport } = fakeTransport([ok({ meters: { enabled: true, intervalMs: 100 } })]);
    expect(await new PolyemesisClient(BASE, "t", transport).meterIntervalMs()).toBe(100);
  });

  it("treats settings with no interval as malformed rather than guessing one", async () => {
    const { transport } = fakeTransport([ok({ meters: {} })]);
    await expect(new PolyemesisClient(BASE, "t", transport).meterIntervalMs()).rejects.toMatchObject({
      kind: "malformed",
    });
  });
});

describe("PolyemesisClient: failures", () => {
  it("names a 401 as unauthorized", async () => {
    const { transport } = fakeTransport([{ status: 401, body: '{"error":"no such token"}' }]);
    await expect(new PolyemesisClient(BASE, "t", transport).connection()).rejects.toMatchObject({
      kind: "unauthorized",
      status: 401,
    });
  });

  it("names a 403 as forbidden, keeping the server's message", async () => {
    const { transport } = fakeTransport([{ status: 403, body: '{"error":"admin scope required"}' }]);
    const err = await new PolyemesisClient(BASE, "t", transport).stopAll().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ kind: "forbidden", message: "admin scope required" });
  });

  it("keeps the server's error text on any other failure", async () => {
    const { transport } = fakeTransport([{ status: 500, body: '{"error":"store is locked"}' }]);
    await expect(new PolyemesisClient(BASE, "t", transport).status()).rejects.toMatchObject({
      kind: "http",
      status: 500,
      message: "store is locked",
    });
  });

  it("falls back to the status code when the body is not the server's error shape", async () => {
    const { transport } = fakeTransport([{ status: 502, body: "<html>bad gateway</html>" }]);
    await expect(new PolyemesisClient(BASE, "t", transport).status()).rejects.toMatchObject({
      kind: "http",
      message: "HTTP 502",
    });
  });

  it("names a 200 with a non-JSON body as malformed", async () => {
    const { transport } = fakeTransport([{ status: 200, body: "<html>" }]);
    await expect(new PolyemesisClient(BASE, "t", transport).status()).rejects.toMatchObject({ kind: "malformed" });
  });

  it("names a refused connection as unreachable", async () => {
    const { transport } = fakeTransport([new TransportError("unreachable", "connect ECONNREFUSED")]);
    await expect(new PolyemesisClient(BASE, "t", transport).connection()).rejects.toMatchObject({
      kind: "unreachable",
    });
  });

  it("names an untrusted certificate as untrusted, not unreachable", async () => {
    const { transport } = fakeTransport([new TransportError("untrusted", "not trusted")]);
    await expect(new PolyemesisClient(BASE, "t", transport).connection()).rejects.toMatchObject({
      kind: "untrusted",
    });
  });
});

describe("classify", () => {
  it.each(["DEPTH_ZERO_SELF_SIGNED_CERT", "SELF_SIGNED_CERT_IN_CHAIN", "UNABLE_TO_VERIFY_LEAF_SIGNATURE"])(
    "treats %s as an untrusted certificate",
    (code) => {
      expect(classify(Object.assign(new Error("x"), { code }))).toMatchObject({ kind: "untrusted" });
    },
  );

  it("treats a socket error as unreachable", () => {
    expect(classify(Object.assign(new Error("connect ECONNREFUSED"), { code: "ECONNREFUSED" }))).toMatchObject({
      kind: "unreachable",
    });
  });
});
