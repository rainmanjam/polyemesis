import http from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, describe, expect, it } from "vitest";
import { TransportError } from "../src/api.js";
import { nodeTransport } from "../src/transport.js";

// The real transport against a local server that never answers: a request's own timeout must be the
// one that applies, because the dump calls wait longer than every other call (the server holds a dump's
// answer until the next keyframe).

let server: http.Server | undefined;

afterEach(async () => {
  server?.closeAllConnections();
  await new Promise<void>((r) => (server ? server.close(() => r()) : r()));
  server = undefined;
});

async function silentServer(): Promise<URL> {
  server = http.createServer(() => {
    // Never answers.
  });
  await new Promise<void>((r) => server!.listen(0, "127.0.0.1", () => r()));
  const { port } = server.address() as AddressInfo;
  return new URL(`http://127.0.0.1:${port}/api/v1/status`);
}

describe("nodeTransport: per-request timeout", () => {
  it("gives up after the request's own timeout, as unreachable", async () => {
    const url = await silentServer();
    const started = Date.now();
    const err = await nodeTransport()
      .send({ method: "GET", url, headers: {}, timeoutMs: 100 })
      .then(
        () => undefined,
        (e: unknown) => e,
      );
    expect(err).toBeInstanceOf(TransportError);
    expect((err as TransportError).kind).toBe("unreachable");
    // Well under the 5 s default: the request's timeout applied, not the default.
    expect(Date.now() - started).toBeLessThan(2000);
  });
});
