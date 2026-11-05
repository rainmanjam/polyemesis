import { afterEach, expect } from "vitest";
import { PolyemesisClient, contractRoute, type Method, type Transport, type TransportRequest, type TransportResponse } from "../src/api.js";
import { NOT_THE_SERVERS_SHAPE, SCHEMA, departures, violations } from "./support/contract.js";

// EVERY FAKE ANSWER IN EVERY TEST IS HELD TO THE SERVER'S SCHEMA.
//
// The tests answer the client from hand-written fakes: runtime.test.ts's route maps, fakeInstall.ts, the
// queues in api.test.ts. A fake written from the Go structs is a copy, and when the server renames a field the
// copy keeps the old name and the suite stays green. So every answer any fake gives the client is checked here
// against test/contract/schema.json, which internal/api/streamdeck_goldens_test.go records from the real
// handlers: a field the server never sends, or a value of a type it never sends there, fails the test that
// served it; a refusal is held to the one error shape the server sends. A fake that is deliberately not the server's shape (a malformed-answer test) says so with
// malformed() from support/contract.ts.
//
// The hook is on the client, below every fake: the first call of each client wraps its transport.

const WRAPPED = Symbol("contract-checked transport");

function check(req: TransportRequest, res: TransportResponse): void {
  if ((res as { [NOT_THE_SERVERS_SHAPE]?: boolean })[NOT_THE_SERVERS_SHAPE]) return;
  if (res.body.trim() === "") return;
  let body: unknown;
  try {
    body = JSON.parse(res.body);
  } catch {
    return; // not JSON: the client's own malformed path, which its tests cover
  }
  // A refusal has one shape whatever the route: {"error": ..., "code": ...}.
  if (res.status >= 400) {
    for (const d of departures(body, SCHEMA.refusal)) violations.push(`${req.method} ${req.url.pathname} ${res.status} ${d}`);
    return;
  }
  if (res.status < 200 || res.status >= 300) return;
  const rel = req.url.pathname.replace(/^.*?\/api\/v1(?=\/)/, "");
  const route = contractRoute(req.method as Method, rel);
  if (!route) return; // the client refuses it before sending; apiContract.test.ts covers that
  const key = `${req.method} ${route.path}`;
  const schema = SCHEMA[key];
  if (!schema) {
    violations.push(`${key}: test/contract/schema.json has no recorded answer for this route`);
    return;
  }
  for (const d of departures(body, schema)) violations.push(`${key} ${d}`);
}

const proto = PolyemesisClient.prototype as unknown as { call(this: { transport: Transport }, ...args: unknown[]): Promise<unknown> };
const realCall = proto.call;
// The client's one way out. If it is renamed, this hook would wrap nothing and every check below would pass
// over nothing; contract.test.ts also proves the hook catches a renamed field.
if (typeof realCall !== "function") throw new Error("test/setup.ts: PolyemesisClient has no call() to hold to the schema");
proto.call = function (this: { transport: Transport & { [WRAPPED]?: true } }, ...args: unknown[]) {
  const inner = this.transport;
  if (!inner[WRAPPED]) {
    const wrapped: Transport & { [WRAPPED]?: true } = {
      async send(req) {
        const res = await inner.send(req);
        check(req, res);
        return res;
      },
    };
    wrapped[WRAPPED] = true;
    this.transport = wrapped;
  }
  return realCall.apply(this, args);
};

afterEach(() => {
  if (violations.length === 0) return;
  const found = [...new Set(violations.splice(0))];
  expect.fail(
    `${expect.getState().currentTestName}: a fake answered with a shape the server does not send ` +
      `(test/contract/schema.json). Fix the fake, or wrap a deliberately malformed one in malformed():\n  ` +
      found.join("\n  "),
  );
});
