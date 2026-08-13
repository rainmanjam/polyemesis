/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

/* The platform registry is served, not mirrored — internal/services/services.json
 * is the only copy of the data, and the UI fetches it from GET /api/v1/services.
 * So the drift risk here is not two catalogues of servers. It is the SHAPE: the
 * TypeScript interface the dialog reads through is hand-written, and a field
 * renamed on the Go side reaches it only as `undefined`.
 *
 * That failure is silent in the worst way. `service.recommended.maxAudioKbps`
 * becoming undefined does not throw and does not fail a typecheck — the ceiling
 * hint simply stops rendering, and the operator sees a dialog that looks
 * complete. Exactly the class preset-drift.test.ts was written for, one layer
 * down.
 *
 * The registry file is the reference because it is what the API serialises.
 */

const ROOT = new URL("../../../", import.meta.url).pathname;

function registryKeys(): { service: string[]; recommended: string[]; server: string[] } {
  const raw = readFileSync(join(ROOT, "internal/services/services.json"), "utf8");
  const doc = JSON.parse(raw) as {
    services: Array<Record<string, unknown> & { recommended: Record<string, unknown> }>;
  };
  const service = new Set<string>();
  const recommended = new Set<string>();
  const server = new Set<string>();
  for (const s of doc.services) {
    for (const k of Object.keys(s)) service.add(k);
    for (const k of Object.keys(s.recommended ?? {})) recommended.add(k);
    for (const sv of (s.servers ?? []) as Array<Record<string, unknown>>) {
      for (const k of Object.keys(sv)) server.add(k);
    }
  }
  return {
    service: [...service],
    recommended: [...recommended],
    server: [...server],
  };
}

/** The field names declared on an interface in types.ts, read as text. A real
 *  parser would be better; this is deliberately the same technique
 *  preset-drift.test.ts uses, so both rot the same visible way. */
function declaredFields(iface: string): string[] {
  const src = readFileSync(join(ROOT, "ui/src/lib/types.ts"), "utf8");
  const start = src.indexOf(`export interface ${iface} {`);
  expect(start, `no interface ${iface} in types.ts`).toBeGreaterThan(-1);
  const end = src.indexOf("\n}", start);
  return [...src.slice(start, end).matchAll(/^\s{2}(\w+)\??:/gm)].map((m) => m[1]);
}

describe("the service registry and the type the UI reads it through", () => {
  it("agree on every field the registry actually ships", () => {
    const keys = registryKeys();
    expect(keys.service.length, "no service keys parsed — services.json did not load").toBeGreaterThan(4);

    const cases: Array<[string, string[]]> = [
      ["ServiceInfo", keys.service],
      ["ServiceRecommended", keys.recommended],
      ["ServiceServer", keys.server],
    ];
    for (const [iface, shipped] of cases) {
      const declared = declaredFields(iface);
      const missing = shipped.filter((k) => !declared.includes(k));
      expect(
        missing,
        `${iface} in types.ts is missing ${missing.join(", ")} — the API sends ` +
          `these and the dialog reads undefined instead, with no typecheck error ` +
          `and no runtime throw.`,
      ).toEqual([]);
    }
  });

  it("keeps the ceiling the audio hint depends on", () => {
    // Named on its own because the hint is the only place a number from this
    // file reaches the operator, and a rename would just stop rendering it.
    const raw = readFileSync(join(ROOT, "internal/services/services.json"), "utf8");
    const doc = JSON.parse(raw) as {
      services: Array<{ name: string; recommended: { maxAudioKbps?: number } }>;
    };
    for (const s of doc.services) {
      expect(
        s.recommended.maxAudioKbps,
        `${s.name} has no maxAudioKbps, so the dialog's audio ceiling hint ` +
          `renders nothing for it`,
      ).toBeGreaterThan(0);
    }
  });
});
