/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

/* An install with no source has no engine, so every live endpoint is served by
 * a nil *engine.Engine. What it answers with is a contract between two
 * languages, and this file pins the half TypeScript cannot check.
 *
 * The specific lie this exists to catch: `Status.renditions` and
 * `Status.destinations` are declared NON-NULLABLE here, so every screen that
 * renders them maps over them without a guard. A Go nil slice marshals to
 * `null`, and `null.map` is a blank page with a stack trace in the console --
 * on the very first load of a fresh install, which is the one moment the
 * operator has no other screen to fall back to. Nothing in a typecheck notices,
 * because the type says it cannot happen.
 *
 * The EXECUTABLE half lives in Go, where the values are:
 * internal/engine/nil_receiver_reads_test.go marshals a zero-source Status and
 * asserts the arrays, and internal/api/zero_source_reads_test.go drives the
 * real route and the WebSocket burst. This file asserts the two ends still
 * describe the same shape -- it is the same technique, and the same limitation,
 * as service-registry-drift.test.ts one layer up.
 */

const ROOT = new URL("../../../", import.meta.url).pathname;

function read(rel: string): string {
  return readFileSync(join(ROOT, rel), "utf8");
}

/** The body of one interface in types.ts, as text. */
function interfaceBody(name: string): string {
  const src = read("ui/src/lib/types.ts");
  const start = src.indexOf(`export interface ${name} {`);
  expect(start, `no interface ${name} in types.ts`).toBeGreaterThan(-1);
  const end = src.indexOf("\n}", start);
  expect(end, `interface ${name} is unterminated`).toBeGreaterThan(start);
  return src.slice(start, end);
}

/** The line declaring one field, without its trailing comment. */
function fieldLine(iface: string, field: string): string {
  const line = interfaceBody(iface)
    .split("\n")
    .find((l) => new RegExp(`^\\s{2}${field}\\??:`).test(l));
  expect(line, `no field ${field} on ${iface}`).toBeDefined();
  return (line as string).trim();
}

describe("the status the dashboard renders on an install with no source", () => {
  it("is declared as arrays the UI may map over without a guard", () => {
    for (const field of ["renditions", "destinations"]) {
      const line = fieldLine("Status", field);
      expect(line, `Status.${field} is optional; the pages that render it do not check`)
        .not.toMatch(/^\w+\?:/);
      expect(line, `Status.${field} admits null; then every .map over it is a crash`)
        .not.toMatch(/\bnull\b/);
      expect(line, `Status.${field} is no longer an array`).toMatch(/\[\]/);
    }
  });

  it("is served as empty arrays by the nil-receiver branch, not as the bare zero value", () => {
    const src = read("internal/engine/status.go");
    const guard = src.slice(
      src.indexOf("func (e *Engine) Status() Status {"),
      src.indexOf("e.mu.RLock()", src.indexOf("func (e *Engine) Status() Status {")),
    );
    expect(guard, "Engine.Status has no nil-receiver branch at all, so an install " +
      "with no source panics before it can answer anything").toContain("if e == nil");
    /* `return Status{}` would satisfy the Go compiler, pass any test that only
     * checks for a 200, and hand the UI two nulls. */
    expect(guard, "the nil-receiver Status does not name an empty renditions slice")
      .toMatch(/Renditions:\s*\[\]RenditionStatus\{\}/);
    expect(guard, "the nil-receiver Status does not name an empty destinations slice")
      .toMatch(/Destinations:\s*\[\]DestStatus\{\}/);
    /* loudness is not on the Status interface here YET, and that is exactly why
     * it is asserted in Go rather than skipped: the field is already on the
     * wire, it has no omitempty, and both GET /api/v1/loudness and
     * Engine.Loudness normalise it to []. Whoever adds it to types.ts will
     * declare it non-nullable to match what a running server sends, and would
     * inherit a null from this one branch on the first load of a fresh install.
     */
    expect(guard, "the nil-receiver Status does not name an empty loudness slice, so it " +
      "sends null for a field every other producer sends [] for")
      .toMatch(/Loudness:\s*\[\]meters\.Report\{\}/);
  });
});

describe("the setup status, the only thing a browser can read before signing in", () => {
  it("carries the source count on both sides", () => {
    const handler = read("internal/api/handlers.go");
    const setup = handler.slice(
      handler.indexOf("func (s *Server) handleSetupStatus("),
      handler.indexOf("func (s *Server) handleSetup("),
    );
    expect(setup, "handleSetupStatus no longer reports how many sources exist; the " +
      "empty state has nothing to key off and a fresh install looks like a broken one")
      .toContain(`body["sources"]`);

    const client = read("ui/src/lib/api.ts");
    const call = client.slice(client.indexOf("setupStatus: () =>"));
    expect(call.slice(0, 200), "the setupStatus client type does not declare sources, " +
      "so the field arrives and TypeScript says it is not there").toMatch(/sources\?:\s*number/);
  });
});
